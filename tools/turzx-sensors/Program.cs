// SPDX-License-Identifier: GPL-3.0-or-later
// Sensor gathering logic uses public LibreHardwareMonitorLib APIs only.

using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Runtime.InteropServices;
using LibreHardwareMonitor.Hardware;
using LibreHardwareMonitor.PawnIo;

namespace TurzxSensors;

internal static class Program
{
    private const int DefaultSamples = 5;
    private const int SleepMilliseconds = 1000;
    private const int MaxStdioLineLength = 32;
    private const int MaxConsecutiveSnapshotFailures = 30;
    private static readonly JsonSerializerOptions JsonOptions = new() { WriteIndented = false };

    private sealed class SensorSnapshot
    {
        [JsonPropertyName("protocol_version")]
        public int ProtocolVersion { get; init; } = 1;

        [JsonPropertyName("observed_at")]
        public string ObservedAt { get; init; } = "";

        [JsonPropertyName("driver_installed")]
        public bool DriverInstalled { get; init; }

        [JsonPropertyName("elevated")]
        public bool Elevated { get; init; }

        [JsonPropertyName("errors")]
        public string[] Errors { get; init; } = Array.Empty<string>();

        [JsonPropertyName("sensors")]
        public SensorPayload[] Sensors { get; init; } = Array.Empty<SensorPayload>();
    }

    private sealed class SensorPayload
    {
        [JsonPropertyName("sensor_id")]
        public string SensorId { get; init; } = "";

        [JsonPropertyName("hardware_id")]
        public string HardwareId { get; init; } = "";

        [JsonPropertyName("hardware_type")]
        public string HardwareType { get; init; } = "";

        [JsonPropertyName("name")]
        public string Name { get; init; } = "";

        [JsonPropertyName("type")]
        public string Type { get; init; } = "";

        [JsonPropertyName("value")]
        public double? Value { get; init; }

        [JsonPropertyName("state")]
        public string State { get; init; } = "error";

        [JsonPropertyName("observed_at")]
        public string? ObservedAt { get; init; }
    }

    private sealed record SensorDriverState(bool IsInstalled, bool Elevated, bool IsWindows, string[] Errors);

    private static async Task<int> Main(string[] args)
    {
        var stdout = Console.Out;
        Console.SetOut(Console.Error);

        var parse = ParseArguments(args);
        if (parse.ParseCode != 0)
        {
            WriteLineStderr(parse.ErrorMessage);
            WriteUsage();
            return parse.ParseCode;
        }

        if (parse.SelfTest)
        {
            var failure = RunSelfTest(out var snapshot);
            if (failure is not null)
            {
                WriteLineStderr(failure);
                return 1;
            }

            WriteJsonLine(stdout, snapshot);
            return 0;
        }

        if (!string.IsNullOrEmpty(parse.SnapshotPath))
        {
            if (!RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
            {
                WriteLineStderr("snapshot mode requires Windows");
                return 1;
            }

            if (!IsElevated())
            {
                WriteLineStderr("snapshot mode requires an elevated process");
                return 1;
            }

            try
            {
                SnapshotFile.ValidatePath(parse.SnapshotPath);
                SnapshotFile.DeleteOrphanedTemporaries(parse.SnapshotPath);
            }
            catch (Exception ex)
            {
                WriteLineStderr(ex.Message);
                return 1;
            }
        }

        using var cts = new CancellationTokenSource();
        Console.CancelKeyPress += (_, e) =>
        {
            e.Cancel = true;
            cts.Cancel();
        };

        var driverState = GetPawnDriverState();
        var computer = new Computer
        {
            IsCpuEnabled = driverState.IsWindows && driverState.Elevated && driverState.IsInstalled,
            IsGpuEnabled = true,
            IsMotherboardEnabled = driverState.IsWindows && driverState.Elevated && driverState.IsInstalled,
            // Never. LibreHardwareMonitor's memory group reaches for DIMM thermal
            // sensors over the SMBus (RAMSPDToolkit), which blocks indefinitely
            // here and has wedged the machine. The only memory sensors it ever
            // returned were load figures the daemon already reads from the OS
            // with no driver at all, so nothing is lost by leaving this off.
            IsMemoryEnabled = false,
            IsStorageEnabled = false,
            IsNetworkEnabled = false,
            IsControllerEnabled = false
        };

        try
        {
            computer.Open();

            if (parse.StdioMode)
            {
                return await RunStdioLoopAsync(computer, stdout, driverState, cts.Token);
            }

            if (!string.IsNullOrEmpty(parse.SnapshotPath))
            {
                return await RunSnapshotLoopAsync(computer, driverState, parse.SnapshotPath, cts.Token);
            }

            return await RunSampleLoopAsync(computer, stdout, driverState, parse.Samples, cts.Token);
        }
        catch (OperationCanceledException)
        {
            return 0;
        }
        catch (Exception ex)
        {
            WriteLineStderr(ex.Message);
            return 1;
        }
        finally
        {
            computer.Close();
        }
    }

    private static (bool StdioMode, bool SelfTest, string SnapshotPath, int Samples, bool SamplesSpecified, int ParseCode, string ErrorMessage) ParseArguments(string[] args)
    {
        var stdioMode = false;
        var selfTest = false;
        var snapshotPath = string.Empty;
        var samples = DefaultSamples;
        var samplesSpecified = false;

        for (var i = 0; i < args.Length; i++)
        {
            switch (args[i])
            {
                case "--stdio":
                    stdioMode = true;
                    break;
                case "--self-test":
                    selfTest = true;
                    break;
                case "--snapshot-file":
                    if (i + 1 >= args.Length || string.IsNullOrWhiteSpace(args[i + 1]))
                    {
                        return (false, false, string.Empty, DefaultSamples, false, 2, "--snapshot-file requires an absolute path");
                    }

                    if (!string.IsNullOrEmpty(snapshotPath))
                    {
                        return (false, false, string.Empty, DefaultSamples, false, 2, "--snapshot-file may be specified only once");
                    }

                    snapshotPath = args[++i];
                    break;
                case "--samples":
                    if (i + 1 >= args.Length)
                    {
                        return (false, false, string.Empty, DefaultSamples, false, 2, "--samples requires numeric argument");
                    }

                    if (!int.TryParse(args[i + 1], out var parsed) || parsed < 0)
                    {
                        return (false, false, string.Empty, DefaultSamples, false, 2, "--samples requires a non-negative integer");
                    }

                    samples = parsed;
                    samplesSpecified = true;
                    i++;
                    break;
                default:
                    return (false, false, string.Empty, DefaultSamples, false, 2, $"unknown argument: {args[i]}");
            }
        }

        var modes = (stdioMode ? 1 : 0) + (selfTest ? 1 : 0) + (!string.IsNullOrEmpty(snapshotPath) ? 1 : 0);
        if (modes > 1 || (modes == 1 && samplesSpecified))
        {
            return (false, false, string.Empty, DefaultSamples, false, 2, "choose one of --stdio, --self-test, --snapshot-file; --samples is valid only with none of them");
        }

        return (stdioMode, selfTest, snapshotPath, samples, samplesSpecified, 0, string.Empty);
    }

    private static void WriteUsage()
    {
        WriteLineStderr("Usage:");
        WriteLineStderr("  turzx-sensors [--samples N]");
        WriteLineStderr("  turzx-sensors --stdio | --self-test | --snapshot-file <absolute path>");
        WriteLineStderr("  --samples N   number of samples, default 5, 0 for continuous");
        WriteLineStderr("  --stdio       read one 'sample' line per snapshot");
        WriteLineStderr("  --self-test   validate JSON schema without hardware access");
        WriteLineStderr("  --snapshot-file  write elevated hardware snapshots every second");
    }

    private static async Task<int> RunSampleLoopAsync(
        Computer computer,
        TextWriter stdout,
        SensorDriverState driverState,
        int samples,
        CancellationToken token)
    {
        var taken = 0;
        while (!token.IsCancellationRequested)
        {
            WriteJsonLine(stdout, CollectSnapshot(computer, driverState));
            taken++;

            if (samples > 0 && taken >= samples)
            {
                return 0;
            }

            await Task.Delay(SleepMilliseconds, token);
        }

        return 0;
    }

    private static async Task<int> RunSnapshotLoopAsync(
        Computer computer,
        SensorDriverState driverState,
        string snapshotPath,
        CancellationToken token)
    {
        var failures = 0;
        while (!token.IsCancellationRequested)
        {
            var json = JsonSerializer.Serialize(CollectSnapshot(computer, driverState), JsonOptions);
            try
            {
                SnapshotFile.WriteAtomic(snapshotPath, json);
                failures = 0;
            }
            catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
            {
                failures++;
                WriteLineStderr($"snapshot write failed ({failures}/{MaxConsecutiveSnapshotFailures}): {ex.Message}");
                if (failures >= MaxConsecutiveSnapshotFailures)
                {
                    return 1;
                }
            }

            await Task.Delay(SleepMilliseconds, token);
        }

        return 0;
    }

    private static async Task<int> RunStdioLoopAsync(
        Computer computer,
        TextWriter stdout,
        SensorDriverState driverState,
        CancellationToken token)
    {
        using var input = Console.OpenStandardInput();
        while (!token.IsCancellationRequested)
        {
            string? line;
            try
            {
                line = await ReadBoundedLineAsync(input, token);
            }
            catch (InvalidDataException ex)
            {
                WriteLineStderr(ex.Message);
                return 2;
            }

            if (line == null)
            {
                return 0;
            }

            if (line != "sample")
            {
                WriteLineStderr($"invalid input: {line}");
                return 2;
            }

            WriteJsonLine(stdout, CollectSnapshot(computer, driverState));
        }

        return 0;
    }

    private static async Task<string?> ReadBoundedLineAsync(Stream input, CancellationToken token)
    {
        var buffer = new byte[1];
        var line = new StringBuilder();
        var lineLength = 0;

        while (true)
        {
            token.ThrowIfCancellationRequested();
            var read = await input.ReadAsync(buffer.AsMemory(0, 1), token);
            if (read == 0)
            {
                if (lineLength != 0) { throw new InvalidDataException("incomplete input line"); }
                return null;
            }

            var ch = (char)buffer[0];
            lineLength++;
            if (lineLength > MaxStdioLineLength)
            {
                throw new InvalidDataException($"invalid input length: {lineLength} > {MaxStdioLineLength}");
            }

            if (ch == '\r')
            {
                continue;
            }

            if (ch == '\n')
            {
                break;
            }

            line.Append((char)buffer[0]);
        }

        return line.ToString();
    }

    private static SensorSnapshot CollectSnapshot(Computer computer, SensorDriverState driverState)
    {
        var observedAt = Timestamp();
        var errors = new List<string>(driverState.Errors);
        var payloads = new List<SensorPayload>();

        foreach (var hardware in computer.Hardware)
        {
            try
            {
                ProcessHardware(hardware, payloads, observedAt, errors, true);
            }
            catch (Exception ex)
            {
                errors.Add($"{hardware.Name}: {ex.Message}");
            }
        }

        return new SensorSnapshot
        {
            ObservedAt = observedAt,
            DriverInstalled = driverState.IsInstalled,
            Elevated = driverState.Elevated,
            Errors = errors.ToArray(),
            Sensors = payloads.ToArray()
        };
    }

    private static void ProcessHardware(IHardware hardware, List<SensorPayload> output, string observedAt, List<string> errors, bool canUpdate)
    {
        if (!canUpdate)
        {
            AddErrorPayloads(hardware, output);

            foreach (var child in hardware.SubHardware)
            {
                ProcessHardware(child, output, observedAt, errors, false);
            }

            return;
        }

        try
        {
            hardware.Update();
        }
        catch (Exception ex)
        {
            errors.Add($"update failed for {hardware.Name}: {ex.Message}");
            AddErrorPayloads(hardware, output);

            foreach (var child in hardware.SubHardware)
            {
                ProcessHardware(child, output, observedAt, errors, false);
            }

            return;
        }

        foreach (var sensor in hardware.Sensors)
        {
            if (!IsTarget(sensor.SensorType))
            {
                continue;
            }

            try
            {
                AddValuePayload(output, hardware, sensor, observedAt);
            }
            catch (Exception ex)
            {
                output.Add(BuildErrorPayload(hardware, sensor));
                errors.Add($"sensor read failed: {hardware.Name}/{sensor.Name}: {ex.Message}");
            }
        }

        foreach (var child in hardware.SubHardware)
        {
            ProcessHardware(child, output, observedAt, errors, true);
        }
    }

    private static void AddValuePayload(List<SensorPayload> output, IHardware hardware, ISensor sensor, string observedAt)
    {
        var value = sensor.Value;
        var isValid = value.HasValue && IsValidValue(sensor.SensorType, value.Value);

        if (!isValid)
        {
            // A null/invalid reading is reported by the sensor's own "error" state only.
            output.Add(BuildErrorPayload(hardware, sensor));
            return;
        }

        output.Add(new SensorPayload
        {
            SensorId = sensor.Identifier?.ToString() ?? string.Empty,
            HardwareId = hardware.Identifier?.ToString() ?? string.Empty,
            HardwareType = hardware.HardwareType.ToString(),
            Name = $"{hardware.Name}/{sensor.Name}",
            Type = sensor.SensorType.ToString(),
            Value = value,
            State = "ok",
            ObservedAt = observedAt
        });
    }

    private static SensorPayload BuildErrorPayload(IHardware hardware, ISensor sensor)
    {
        return new SensorPayload
        {
            SensorId = sensor.Identifier?.ToString() ?? string.Empty,
            HardwareId = hardware.Identifier?.ToString() ?? string.Empty,
            HardwareType = hardware.HardwareType.ToString(),
            Name = $"{hardware.Name}/{sensor.Name}",
            Type = sensor.SensorType.ToString(),
            Value = null,
            State = "error",
            ObservedAt = null
        };
    }

    private static void AddErrorPayloads(IHardware hardware, List<SensorPayload> output)
    {
        foreach (var sensor in hardware.Sensors)
        {
            if (!IsTarget(sensor.SensorType))
            {
                continue;
            }

            output.Add(BuildErrorPayload(hardware, sensor));
        }
    }

    private static bool IsTarget(SensorType type) => type == SensorType.Load || type == SensorType.Temperature;

    private static bool IsValidValue(SensorType type, double value)
    {
        if (!double.IsFinite(value))
        {
            return false;
        }

        return type switch
        {
            SensorType.Load => value is >= 0 and <= 100,
            SensorType.Temperature => value >= -273.15,
            _ => false
        };
    }

    private static SensorDriverState GetPawnDriverState()
    {
        var errors = new List<string>();

        if (!RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
        {
            errors.Add("non-windows platform");
            return new SensorDriverState(false, false, false, errors.ToArray());
        }

        var isAdmin = IsElevated();
        if (!isAdmin)
        {
            errors.Add("not running as administrator");
        }

        bool installed;
        try
        {
            installed = PawnIo.IsInstalled;
            _ = PawnIo.Version;
            if (!installed)
            {
                errors.Add("PawnIo driver is not installed");
            }
        }
        catch (Exception ex)
        {
            installed = false;
            errors.Add($"PawnIo check failed: {ex.Message}");
        }

        if (isAdmin && !installed)
        {
            errors.Add("core sensors disabled by PawnIo state");
        }

        return new SensorDriverState(installed, isAdmin, true, errors.ToArray());
    }

    private static bool IsElevated()
    {
        try
        {
            using var identity = System.Security.Principal.WindowsIdentity.GetCurrent();
            var principal = new System.Security.Principal.WindowsPrincipal(identity);
            return principal.IsInRole(System.Security.Principal.WindowsBuiltInRole.Administrator);
        }
        catch
        {
            return false;
        }
    }

    // Self-test: JSON round-trip of a small snapshot and WriteAtomic rejecting a
    // directory target. Returns a failure reason or null.
    private static string? RunSelfTest(out SensorSnapshot snapshot)
    {
        var now = Timestamp();
        snapshot = new SensorSnapshot
        {
            ObservedAt = now,
            Elevated = IsElevated(),
            Sensors = new[]
            {
                new SensorPayload { SensorId = "selftest-load-cpu", HardwareId = "selftest-cpu", HardwareType = "Cpu", Name = "CPU Load", Type = "Load", Value = 42, State = "ok", ObservedAt = now },
                new SensorPayload { SensorId = "selftest-error", HardwareId = "selftest-cpu", HardwareType = "Cpu", Name = "CPU Invalid", Type = "Load", Value = null, State = "error", ObservedAt = null }
            }
        };

        var parsed = JsonSerializer.Deserialize<SensorSnapshot>(JsonSerializer.Serialize(snapshot, JsonOptions), JsonOptions);
        if (parsed is null || parsed.ProtocolVersion != 1 || parsed.ObservedAt != now || parsed.Sensors.Length != 2 ||
            parsed.Sensors[0].SensorId != "selftest-load-cpu" || parsed.Sensors[0].Value != 42 || parsed.Sensors[0].State != "ok" || parsed.Sensors[0].ObservedAt != now ||
            parsed.Sensors[1].SensorId != "selftest-error" || parsed.Sensors[1].Value is not null || parsed.Sensors[1].State != "error" || parsed.Sensors[1].ObservedAt is not null)
        {
            return "self-test JSON round-trip mismatch";
        }

        try
        {
            SnapshotFile.WriteAtomic(Path.GetTempPath().TrimEnd(Path.DirectorySeparatorChar), "{}");
            return "self-test: WriteAtomic accepted a directory target";
        }
        catch (IOException)
        {
            // Expected: a directory target is rejected before any temp file is made.
        }

        return null;
    }

    private static string Timestamp() => DateTime.UtcNow.ToString("o");

    private static void WriteJsonLine(TextWriter stdout, SensorSnapshot snapshot)
    {
        var json = JsonSerializer.Serialize(snapshot, JsonOptions);
        stdout.WriteLine(json);
        stdout.Flush();
    }

    private static void WriteLineStderr(string message) => Console.Error.WriteLine(message);
}
