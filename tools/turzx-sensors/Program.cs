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
    private static readonly JsonSerializerOptions JsonOptions = new() { WriteIndented = false };

    private sealed class SensorSnapshot
    {
        [JsonPropertyName("protocol_version")]
        public int ProtocolVersion { get; init; } = 1;

        [JsonPropertyName("observed_at")]
        public string ObservedAt { get; init; } = Timestamp();

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
            var snapshot = BuildSelfTestSnapshot();
            if (!ValidateSelfTest(snapshot, out var reason))
            {
                WriteLineStderr(reason ?? "self-test validation failed");
                return 1;
            }

            WriteJsonLine(stdout, snapshot);
            return 0;
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
            IsMemoryEnabled = driverState.IsWindows && driverState.Elevated && driverState.IsInstalled,
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

    private static (bool StdioMode, bool SelfTest, int Samples, int ParseCode, string ErrorMessage) ParseArguments(string[] args)
    {
        var stdioMode = false;
        var selfTest = false;
        var samples = DefaultSamples;

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
                case "--samples":
                    if (i + 1 >= args.Length)
                    {
                        return (false, false, DefaultSamples, 2, "--samples requires numeric argument");
                    }

                    if (!int.TryParse(args[i + 1], out var parsed) || parsed < 0)
                    {
                        return (false, false, DefaultSamples, 2, "--samples requires a non-negative integer");
                    }

                    samples = parsed;
                    i++;
                    break;
                default:
                    return (false, false, DefaultSamples, 2, $"unknown argument: {args[i]}");
            }
        }

        return (stdioMode, selfTest, samples, 0, string.Empty);
    }

    private static void WriteUsage()
    {
        WriteLineStderr("Usage:");
        WriteLineStderr("  turzx-sensors [--samples N] [--stdio] [--self-test]");
        WriteLineStderr("  --samples N   number of samples, default 5, 0 for continuous");
        WriteLineStderr("  --stdio       read one 'sample' line per snapshot");
        WriteLineStderr("  --self-test   validate JSON schema without hardware access");
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
            WriteJsonLine(stdout, CollectSnapshot(computer, driverState, new List<string>(driverState.Errors)));
            taken++;

            if (samples > 0 && taken >= samples)
            {
                return 0;
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
            var line = await ReadBoundedLineAsync(input, token);
            if (line == null)
            {
                return 0;
            }

            if (line != "sample")
            {
                WriteLineStderr($"invalid input: {line}");
                return 2;
            }

            WriteJsonLine(stdout, CollectSnapshot(computer, driverState, new List<string>(driverState.Errors)));
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
                WriteLineStderr($"invalid input length: {lineLength} > {MaxStdioLineLength}");
                return "__line_too_long__";
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

    private static SensorSnapshot CollectSnapshot(Computer computer, SensorDriverState driverState, List<string> sharedErrors)
    {
        var observedAt = Timestamp();
        var errors = new List<string>(sharedErrors);
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
                AddValuePayload(output, errors, hardware, sensor, observedAt);
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

    private static void AddValuePayload(List<SensorPayload> output, List<string> errors, IHardware hardware, ISensor sensor, string observedAt)
    {
        var value = sensor.Value;
        var isValid = value.HasValue && IsValidValue(sensor.SensorType, value.Value);

        if (!isValid)
        {
            output.Add(BuildErrorPayload(hardware, sensor));
            errors.Add($"invalid sensor value: {hardware.Name}/{sensor.Name}");
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

    private static SensorSnapshot BuildSelfTestSnapshot()
    {
        var now = Timestamp();
        return new SensorSnapshot
        {
            ObservedAt = now,
            DriverInstalled = false,
            Elevated = IsElevated(),
            Errors = Array.Empty<string>(),
            Sensors = new[]
            {
                new SensorPayload
                {
                    SensorId = "selftest-load-cpu",
                    HardwareId = "selftest-cpu",
                    HardwareType = "Cpu",
                    Name = "CPU Load",
                    Type = "Load",
                    Value = 42,
                    State = "ok",
                    ObservedAt = now
                },
                new SensorPayload
                {
                    SensorId = "selftest-temp-cpu",
                    HardwareId = "selftest-cpu",
                    HardwareType = "Cpu",
                    Name = "CPU Temperature",
                    Type = "Temperature",
                    Value = 55,
                    State = "ok",
                    ObservedAt = now
                },
                new SensorPayload
                {
                    SensorId = "selftest-error",
                    HardwareId = "selftest-cpu",
                    HardwareType = "Cpu",
                    Name = "CPU Invalid",
                    Type = "Load",
                    Value = null,
                    State = "error",
                    ObservedAt = null
                }
            }
        };
    }

    private static bool ValidateSelfTest(SensorSnapshot snapshot, out string? reason)
    {
        reason = null;
        if (snapshot.ProtocolVersion != 1)
        {
            reason = "protocol_version must be 1";
            return false;
        }

        if (IsValidValue(SensorType.Load, double.NaN) || IsValidValue(SensorType.Load, -1) || IsValidValue(SensorType.Load, 101))
        {
            reason = "Load validation checks failed";
            return false;
        }

        if (!IsValidValue(SensorType.Load, 0) || !IsValidValue(SensorType.Temperature, -273.15) || IsValidValue(SensorType.Temperature, -274))
        {
            reason = "Temperature/Load range checks failed";
            return false;
        }

        if (snapshot.Sensors.Length < 2)
        {
            reason = "self-test sensors missing";
            return false;
        }

        foreach (var sensor in snapshot.Sensors)
        {
            if (!IsTarget(ParseSensorType(sensor.Type)))
            {
                reason = $"unsupported sensor type: {sensor.Type}";
                return false;
            }

            if (sensor.State == "ok")
            {
                if (sensor.Value is null)
                {
                    reason = $"sensor {sensor.SensorId} missing value";
                    return false;
                }

                if (!IsValidValue(ParseSensorType(sensor.Type), sensor.Value.Value))
                {
                    reason = $"sensor {sensor.SensorId} value invalid";
                    return false;
                }

                if (sensor.ObservedAt is null)
                {
                    reason = $"sensor {sensor.SensorId} missing observed_at";
                    return false;
                }
            }
            else
            {
                if (sensor.Value is not null)
                {
                    reason = $"sensor {sensor.SensorId} should keep value null on error";
                    return false;
                }

                if (sensor.ObservedAt is not null)
                {
                    reason = $"sensor {sensor.SensorId} should keep observed_at null on error";
                    return false;
                }
            }
        }

        var rendered = JsonSerializer.Serialize(snapshot, JsonOptions);
        using var document = JsonDocument.Parse(rendered);
        if (!document.RootElement.TryGetProperty("sensors", out var sensorsElement) || sensorsElement.ValueKind != JsonValueKind.Array)
        {
            reason = "self-test JSON missing sensors";
            return false;
        }

        var hasErrorNullEntry = sensorsElement
            .EnumerateArray()
            .Any(item =>
                item.GetProperty("state").GetString() == "error" &&
                item.TryGetProperty("value", out var value) && value.ValueKind == JsonValueKind.Null &&
                item.TryGetProperty("observed_at", out var observedAt) && observedAt.ValueKind == JsonValueKind.Null);

        if (!hasErrorNullEntry)
        {
            reason = "self-test JSON must include null value and null observed_at for error sensor";
            return false;
        }

        return true;
    }

    private static SensorType ParseSensorType(string type)
    {
        return Enum.Parse<SensorType>(type, ignoreCase: true);
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
