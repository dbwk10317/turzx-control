// SPDX-License-Identifier: GPL-3.0-or-later

namespace TurzxSensors;

internal static class SnapshotFile
{
    internal static void ValidatePath(string path)
    {
        if (!Path.IsPathFullyQualified(path))
        {
            throw new ArgumentException("snapshot path must be absolute", nameof(path));
        }

        var fullPath = Path.GetFullPath(path);
        var parent = Directory.GetParent(fullPath)?.FullName;
        if (string.IsNullOrEmpty(parent) || !Directory.Exists(parent))
        {
            throw new IOException("snapshot parent directory does not exist");
        }

        for (var current = new DirectoryInfo(parent); current is not null; current = current.Parent)
        {
            if (HasReparsePoint(current.FullName))
            {
                throw new IOException("snapshot parent directory must not be a reparse point");
            }
        }

        try
        {
            var attributes = File.GetAttributes(fullPath);
            if ((attributes & FileAttributes.ReparsePoint) != 0 || (attributes & FileAttributes.Directory) != 0)
            {
                throw new IOException("snapshot target must be a regular file without a reparse point");
            }
        }
        catch (FileNotFoundException)
        {
            // The target may be created by the first snapshot write.
        }
        catch (DirectoryNotFoundException)
        {
            throw new IOException("snapshot parent directory does not exist");
        }
    }

    internal static void WriteAtomic(string path, string content)
    {
        ValidatePath(path);
        var fullPath = Path.GetFullPath(path);
        var parent = Directory.GetParent(fullPath)!.FullName;
        var temporary = Path.Combine(parent, $".{Path.GetFileName(fullPath)}.{Guid.NewGuid():N}.tmp");
        var created = false;

        try
        {
            var bytes = System.Text.Encoding.UTF8.GetBytes(content);
            using (var stream = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None, 4096, FileOptions.WriteThrough))
            {
                created = true;
                stream.Write(bytes, 0, bytes.Length);
                stream.Flush(flushToDisk: true);
            }

            ValidatePath(fullPath);
            File.Move(temporary, fullPath, overwrite: true);
        }
        finally
        {
            try
            {
                if (created && File.Exists(temporary))
                {
                    File.Delete(temporary);
                }
            }
            catch
            {
                // Preserve the original write/move error; the next start can
                // remove an orphaned temp file after the parent is inspected.
            }
        }
    }

    private static bool HasReparsePoint(string path)
    {
        return (File.GetAttributes(path) & FileAttributes.ReparsePoint) != 0;
    }
}
