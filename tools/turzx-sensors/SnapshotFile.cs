// SPDX-License-Identifier: GPL-3.0-or-later

using System.Security.AccessControl;
using System.Security.Principal;

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

    // Removes temp files left behind by an interrupted WriteAtomic. Call once at
    // startup after ValidatePath; per-file failures are ignored.
    internal static void DeleteOrphanedTemporaries(string path)
    {
        var fullPath = Path.GetFullPath(path);
        var parent = Directory.GetParent(fullPath)!.FullName;
        foreach (var orphan in Directory.EnumerateFiles(parent, $".{Path.GetFileName(fullPath)}.*.tmp", SearchOption.TopDirectoryOnly))
        {
            try
            {
                File.Delete(orphan);
            }
            catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
            {
                // Best effort; the file is not part of the published snapshot.
            }
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
            // No WriteThrough/flush-to-disk: the file is IPC replaced every second.
            using (var stream = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None))
            {
                created = true;
                stream.Write(bytes, 0, bytes.Length);
            }

            EnsureAdministratorOwner(temporary);
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
                // Preserve the original write/move error; DeleteOrphanedTemporaries
                // removes the leftover on the next start after the parent is inspected.
            }
        }
    }

    // The Go reader requires an Administrators or SYSTEM owner, which an
    // elevated writer already gets from the default Windows policy; the owner is
    // rewritten only under the NoDefaultAdminOwner policy. It is set on the
    // closed file because a write handle carries no WRITE_OWNER right, while the
    // inherited Administrators ACE on the protected directory grants it.
    // File.Move preserves the owner.
    private static void EnsureAdministratorOwner(string path)
    {
        var file = new FileInfo(path);
        var owner = file.GetAccessControl(AccessControlSections.Owner).GetOwner(typeof(SecurityIdentifier)) as SecurityIdentifier;
        if (owner is not null &&
            (owner.IsWellKnown(WellKnownSidType.BuiltinAdministratorsSid) || owner.IsWellKnown(WellKnownSidType.LocalSystemSid)))
        {
            return;
        }

        var security = new FileSecurity();
        security.SetOwner(new SecurityIdentifier(WellKnownSidType.BuiltinAdministratorsSid, null));
        file.SetAccessControl(security);
    }

    private static bool HasReparsePoint(string path)
    {
        return (File.GetAttributes(path) & FileAttributes.ReparsePoint) != 0;
    }
}
