using System;
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using UnityEngine;
using Debug = UnityEngine.Debug;

namespace Frostebite.ElasticGitStorage.Editor
{
    /// <summary>
    /// Static utility class for interacting with git config and locating the
    /// elastic-git-storage binary. All git commands run with the working directory
    /// set to the Unity project root (the parent of Application.dataPath).
    /// </summary>
    public static class GitConfigHelper
    {
        /// <summary>
        /// Returns the Unity project root directory (parent of Assets/).
        /// </summary>
        static string ProjectRoot =>
            Directory.GetParent(Application.dataPath).FullName;

        // -------------------------------------------------------------------
        // Git config helpers
        // -------------------------------------------------------------------

        /// <summary>
        /// Reads a git config value. Returns null if the key is not set.
        /// </summary>
        public static string GetConfig(string key)
        {
            var (exitCode, stdout, _) = RunProcess("git", $"config --get {key}", ProjectRoot);
            if (exitCode != 0)
                return null;

            string value = stdout?.Trim();
            return string.IsNullOrEmpty(value) ? null : value;
        }

        /// <summary>
        /// Reads a git config boolean value. Returns null if the key is not set.
        /// </summary>
        public static bool? GetConfigBool(string key)
        {
            var (exitCode, stdout, _) = RunProcess("git", $"config --bool --get {key}", ProjectRoot);
            if (exitCode != 0)
                return null;

            string value = stdout?.Trim();
            if (string.IsNullOrEmpty(value))
                return null;

            return string.Equals(value, "true", StringComparison.OrdinalIgnoreCase);
        }

        /// <summary>
        /// Sets a git config value. Returns true on success.
        /// </summary>
        public static bool SetConfig(string key, string value)
        {
            var (exitCode, _, _) = RunProcess("git", $"config {key} \"{value}\"", ProjectRoot);
            return exitCode == 0;
        }

        /// <summary>
        /// Sets a git config boolean value. Returns true on success.
        /// </summary>
        public static bool SetConfigBool(string key, bool value)
        {
            string boolStr = value ? "true" : "false";
            var (exitCode, _, _) = RunProcess("git", $"config {key} {boolStr}", ProjectRoot);
            return exitCode == 0;
        }

        /// <summary>
        /// Unsets a git config key. Returns true on success (including when
        /// the key was already absent).
        /// </summary>
        public static bool UnsetConfig(string key)
        {
            var (exitCode, _, _) = RunProcess("git", $"config --unset {key}", ProjectRoot);
            // Exit code 5 means the key was not set — that counts as success.
            return exitCode == 0 || exitCode == 5;
        }

        // -------------------------------------------------------------------
        // Repository helpers
        // -------------------------------------------------------------------

        /// <summary>
        /// Returns the repository root path via <c>git rev-parse --show-toplevel</c>.
        /// </summary>
        public static string GetRepoRoot()
        {
            var (exitCode, stdout, _) = RunProcess("git", "rev-parse --show-toplevel", ProjectRoot);
            if (exitCode != 0)
                return null;

            return stdout?.Trim();
        }

        /// <summary>
        /// Checks whether Git LFS is installed and returns its version string.
        /// </summary>
        public static (bool installed, string version) CheckGitLfs()
        {
            var (exitCode, stdout, _) = RunProcess("git", "lfs version", ProjectRoot);
            if (exitCode != 0)
                return (false, null);

            string version = stdout?.Trim();
            return (true, version);
        }

        // -------------------------------------------------------------------
        // Binary discovery
        // -------------------------------------------------------------------

        /// <summary>
        /// Locates the elastic-git-storage binary. Checks the package's
        /// <c>Editor/Bin~/</c> folder first, then falls back to the system PATH.
        /// Returns the full path to the binary, or null if not found.
        /// </summary>
        public static string FindBinary(string packageBinPath)
        {
            string binaryName = GetBinaryName();
            if (binaryName == null)
                return null;

            // 1. Check the package's Bin~ folder.
            if (!string.IsNullOrEmpty(packageBinPath))
            {
                string candidate = Path.Combine(packageBinPath, binaryName);
                if (File.Exists(candidate))
                    return Path.GetFullPath(candidate);
            }

            // 2. Check PATH via where (Windows) or which (macOS/Linux).
            string whichCmd;
            string whichArgs;

            if (Application.platform == RuntimePlatform.WindowsEditor)
            {
                whichCmd = "where";
                whichArgs = "elastic-git-storage";
            }
            else
            {
                whichCmd = "which";
                whichArgs = "elastic-git-storage";
            }

            var (exitCode, stdout, _) = RunProcess(whichCmd, whichArgs, ProjectRoot);
            if (exitCode == 0 && !string.IsNullOrEmpty(stdout))
            {
                // 'where' on Windows may return multiple lines; take the first.
                string firstLine = stdout.Split(new[] { '\r', '\n' }, StringSplitOptions.RemoveEmptyEntries)[0].Trim();
                if (File.Exists(firstLine))
                    return firstLine;
            }

            return null;
        }

        /// <summary>
        /// Returns the platform-specific binary file name.
        /// </summary>
        static string GetBinaryName()
        {
            if (Application.platform == RuntimePlatform.WindowsEditor)
                return "elastic-git-storage.exe";

            return "elastic-git-storage";
        }

        // -------------------------------------------------------------------
        // Process runner
        // -------------------------------------------------------------------

        /// <summary>
        /// Runs an external process and captures its output.
        /// </summary>
        /// <param name="executable">The executable to run.</param>
        /// <param name="arguments">Command-line arguments.</param>
        /// <param name="workingDirectory">Working directory for the process.</param>
        /// <param name="timeoutMs">Timeout in milliseconds (default 30 000).</param>
        /// <returns>A tuple of (exitCode, stdout, stderr).</returns>
        public static (int exitCode, string stdout, string stderr) RunProcess(
            string executable,
            string arguments,
            string workingDirectory,
            int timeoutMs = 30000)
        {
            try
            {
                using (var process = new Process())
                {
                    process.StartInfo = new ProcessStartInfo
                    {
                        FileName = executable,
                        Arguments = arguments,
                        WorkingDirectory = workingDirectory,
                        CreateNoWindow = true,
                        UseShellExecute = false,
                        RedirectStandardOutput = true,
                        RedirectStandardError = true
                    };

                    process.Start();

                    // Read stdout and stderr to avoid deadlocks on large output.
                    string stdout = process.StandardOutput.ReadToEnd();
                    string stderr = process.StandardError.ReadToEnd();

                    bool exited = process.WaitForExit(timeoutMs);
                    if (!exited)
                    {
                        try { process.Kill(); }
                        catch { /* best effort */ }

                        return (-1, stdout, "Process timed out");
                    }

                    return (process.ExitCode, stdout, stderr);
                }
            }
            catch (Exception ex)
            {
                return (-1, null, ex.Message);
            }
        }

        // -------------------------------------------------------------------
        // Platform detection (mirrors CI asset naming)
        // -------------------------------------------------------------------

        /// <summary>
        /// Returns the OS identifier used in release asset names
        /// (windows, linux, darwin), or null if unsupported.
        /// </summary>
        public static string GetPlatformOs()
        {
            switch (SystemInfo.operatingSystemFamily)
            {
                case OperatingSystemFamily.Windows: return "windows";
                case OperatingSystemFamily.Linux:   return "linux";
                case OperatingSystemFamily.MacOSX:  return "darwin";
                default:                            return null;
            }
        }

        /// <summary>
        /// Returns the architecture identifier used in release asset names
        /// (amd64, arm64), or null if unsupported.
        /// </summary>
        public static string GetPlatformArch()
        {
            switch (RuntimeInformation.ProcessArchitecture)
            {
                case Architecture.X64:   return "amd64";
                case Architecture.Arm64: return "arm64";
                default:                 return null;
            }
        }

        /// <summary>
        /// Returns the full asset name for the current platform, e.g.
        /// <c>elastic-git-storage-windows-amd64.zip</c>, or null if the
        /// platform is unsupported.
        /// </summary>
        public static string GetPlatformAssetName()
        {
            string os = GetPlatformOs();
            string arch = GetPlatformArch();
            if (os == null || arch == null)
                return null;

            return $"elastic-git-storage-{os}-{arch}.zip";
        }

        /// <summary>
        /// Queries the installed binary for its version string
        /// (runs <c>elastic-git-storage --version</c>).
        /// Returns null if the binary is not found or the command fails.
        /// </summary>
        public static string GetBinaryVersion(string binaryPath)
        {
            if (string.IsNullOrEmpty(binaryPath) || !File.Exists(binaryPath))
                return null;

            var (exitCode, stdout, stderr) = RunProcess(binaryPath, "--version", ProjectRoot, 5000);

            // The binary writes version to stderr (os.Stderr.WriteString).
            string output = !string.IsNullOrEmpty(stderr) ? stderr.Trim() : stdout?.Trim();

            if (exitCode != 0 && string.IsNullOrEmpty(output))
                return null;

            return string.IsNullOrEmpty(output) ? null : output;
        }
    }
}
