using System;
using System.IO;
using System.IO.Compression;
using System.Runtime.InteropServices;
using UnityEditor;
using UnityEngine;
using UnityEngine.Networking;

namespace Frostebite.ElasticGitStorage.Editor
{
    /// <summary>
    /// Editor window for managing the elastic-git-storage binary, inspecting
    /// and editing git configuration, and validating the local setup.
    /// </summary>
    public sealed class ElasticGitStorageWindow : EditorWindow
    {
        // -----------------------------------------------------------------
        // Constants
        // -----------------------------------------------------------------

        const string k_LatestReleaseApi =
            "https://api.github.com/repos/frostebite/elastic-git-storage/releases/latest";

        const string k_EditorPrefLatestTag  = "ElasticGitStorage_LatestTag";
        const string k_EditorPrefLatestUrl  = "ElasticGitStorage_LatestUrl";
        const string k_TransferName         = "elastic-git-storage";

        // Git config keys managed by this window.
        const string k_CfgPath     = "lfs.customtransfer.elastic-git-storage.path";
        const string k_CfgArgs     = "lfs.customtransfer.elastic-git-storage.args";
        const string k_CfgAgent    = "lfs.standalonetransferagent";
        const string k_CfgPullMain = "lfs.folderstore.pullmain";
        const string k_CfgPushMain = "lfs.folderstore.pushmain";
        const string k_CfgWriteAll = "lfs.folderstore.writeall";

        // -----------------------------------------------------------------
        // Serialised state (survives domain reloads)
        // -----------------------------------------------------------------

        Vector2 _scrollPos;

        // Section 1 — Status
        string _binaryPath;
        string _binaryVersion;
        string _repoRoot;

        // Section 2 — Install / Update
        string _latestTag;
        string _latestAssetUrl;
        string _installStatus = "Ready";
        float  _downloadProgress;
        bool   _isBusy;

        // Section 3 — Configuration (editable form)
        string _storagePath = "";
        string _pushPath    = "";
        int    _compressionIndex; // 0 = none, 1 = zip, 2 = lz4
        bool   _pullMain;
        bool   _pushMain;
        bool   _writeAll;

        // Section 3 — Configuration (read-only display)
        string _cfgPathValue;
        string _cfgArgsValue;
        string _cfgAgentValue;
        string _cfgPullMainValue;
        string _cfgPushMainValue;
        string _cfgWriteAllValue;

        // Section 4 — Validation results
        string[] _validationResults;

        // Download async state
        UnityWebRequest _activeRequest;

        static readonly string[] k_CompressionOptions = { "none", "zip", "lz4" };

        // -----------------------------------------------------------------
        // Menu item
        // -----------------------------------------------------------------

        [MenuItem("Window/elastic-git-storage")]
        public static void ShowWindow()
        {
            var window = GetWindow<ElasticGitStorageWindow>("elastic-git-storage");
            window.minSize = new Vector2(400, 500);
        }

        // -----------------------------------------------------------------
        // Lifecycle
        // -----------------------------------------------------------------

        void OnEnable()
        {
            _latestTag      = EditorPrefs.GetString(k_EditorPrefLatestTag, "");
            _latestAssetUrl = EditorPrefs.GetString(k_EditorPrefLatestUrl, "");
            RefreshStatus();
            RefreshConfigDisplay();
        }

        void OnDisable()
        {
            AbortActiveRequest();
        }

        void Update()
        {
            // Drive the async download forward.
            if (_activeRequest != null && _activeRequest.isDone)
            {
                OnDownloadCompleted();
            }
            else if (_activeRequest != null)
            {
                _downloadProgress = _activeRequest.downloadProgress;
                Repaint();
            }
        }

        // -----------------------------------------------------------------
        // GUI
        // -----------------------------------------------------------------

        void OnGUI()
        {
            _scrollPos = EditorGUILayout.BeginScrollView(_scrollPos);

            DrawStatusSection();
            GUILayout.Space(12);
            DrawInstallSection();
            GUILayout.Space(12);
            DrawConfigSection();
            GUILayout.Space(12);
            DrawValidationSection();

            EditorGUILayout.EndScrollView();
        }

        // =================================================================
        // Section 1: Status
        // =================================================================

        void DrawStatusSection()
        {
            EditorGUILayout.LabelField("Status", EditorStyles.boldLabel);
            EditorGUI.indentLevel++;

            if (!string.IsNullOrEmpty(_binaryPath))
            {
                string versionDisplay = !string.IsNullOrEmpty(_binaryVersion) ? _binaryVersion : "(version unknown)";
                EditorGUILayout.LabelField("Binary", $"Installed  {versionDisplay}");
                EditorGUILayout.LabelField("Path", _binaryPath);
            }
            else
            {
                EditorGUILayout.LabelField("Binary", "Not installed");
            }

            string platformDisplay = GitConfigHelper.GetPlatformAssetName() ?? "unsupported";
            EditorGUILayout.LabelField("Platform", platformDisplay);
            EditorGUILayout.LabelField("Repo Root", _repoRoot ?? "(not detected)");

            EditorGUI.indentLevel--;

            if (GUILayout.Button("Refresh", GUILayout.Width(100)))
            {
                RefreshStatus();
                RefreshConfigDisplay();
            }
        }

        // =================================================================
        // Section 2: Install / Update Binary
        // =================================================================

        void DrawInstallSection()
        {
            EditorGUILayout.LabelField("Install / Update Binary", EditorStyles.boldLabel);
            EditorGUI.indentLevel++;

            if (!string.IsNullOrEmpty(_latestTag))
                EditorGUILayout.LabelField("Latest Release", _latestTag);
            else
                EditorGUILayout.LabelField("Latest Release", "(not checked)");

            EditorGUI.indentLevel--;

            EditorGUILayout.BeginHorizontal();

            using (new EditorGUI.DisabledScope(_isBusy))
            {
                if (GUILayout.Button("Check Latest", GUILayout.Width(120)))
                    FetchLatestRelease();

                bool canDownload = !string.IsNullOrEmpty(_latestAssetUrl);
                using (new EditorGUI.DisabledScope(!canDownload))
                {
                    if (GUILayout.Button("Download & Install", GUILayout.Width(150)))
                        StartDownload();
                }
            }

            EditorGUILayout.EndHorizontal();

            // Progress / status
            if (_isBusy && _activeRequest != null)
            {
                EditorGUI.ProgressBar(
                    EditorGUILayout.GetControlRect(false, 18),
                    _downloadProgress,
                    $"Downloading... {Mathf.RoundToInt(_downloadProgress * 100)}%");
            }

            if (!string.IsNullOrEmpty(_installStatus))
            {
                MessageType msgType = _installStatus.StartsWith("Error")
                    ? MessageType.Error
                    : (_installStatus.Contains("successfully") ? MessageType.Info : MessageType.None);

                EditorGUILayout.HelpBox(_installStatus, msgType);
            }
        }

        // =================================================================
        // Section 3: Git Configuration
        // =================================================================

        void DrawConfigSection()
        {
            EditorGUILayout.LabelField("Git Configuration", EditorStyles.boldLabel);

            // --- Read-only display ---
            EditorGUILayout.LabelField("Current Config Values", EditorStyles.miniBoldLabel);
            EditorGUI.indentLevel++;

            DrawSelectableConfigRow(k_CfgPath,     _cfgPathValue);
            DrawSelectableConfigRow(k_CfgArgs,     _cfgArgsValue);
            DrawSelectableConfigRow(k_CfgAgent,    _cfgAgentValue);
            DrawSelectableConfigRow(k_CfgPullMain, _cfgPullMainValue);
            DrawSelectableConfigRow(k_CfgPushMain, _cfgPushMainValue);
            DrawSelectableConfigRow(k_CfgWriteAll, _cfgWriteAllValue);

            EditorGUI.indentLevel--;

            GUILayout.Space(8);

            // --- Editable form ---
            EditorGUILayout.LabelField("Edit Configuration", EditorStyles.miniBoldLabel);
            EditorGUI.indentLevel++;

            // Storage Path
            EditorGUILayout.BeginHorizontal();
            _storagePath = EditorGUILayout.TextField("Storage Path", _storagePath);
            if (GUILayout.Button("Browse", GUILayout.Width(60)))
            {
                string selected = EditorUtility.OpenFolderPanel("Select Storage Directory", _storagePath, "");
                if (!string.IsNullOrEmpty(selected))
                    _storagePath = selected;
            }
            EditorGUILayout.EndHorizontal();

            // Push Path
            EditorGUILayout.BeginHorizontal();
            _pushPath = EditorGUILayout.TextField("Push Path (optional)", _pushPath);
            if (GUILayout.Button("Browse", GUILayout.Width(60)))
            {
                string selected = EditorUtility.OpenFolderPanel("Select Push Directory", _pushPath, "");
                if (!string.IsNullOrEmpty(selected))
                    _pushPath = selected;
            }
            EditorGUILayout.EndHorizontal();

            // Compression
            _compressionIndex = EditorGUILayout.Popup("Compression", _compressionIndex, k_CompressionOptions);

            // Boolean flags
            _pullMain = EditorGUILayout.Toggle("Fallback pull from main LFS server", _pullMain);
            _pushMain = EditorGUILayout.Toggle("Also push to main LFS server", _pushMain);
            _writeAll = EditorGUILayout.Toggle("Write to all destinations", _writeAll);

            EditorGUI.indentLevel--;

            GUILayout.Space(4);

            EditorGUILayout.BeginHorizontal();

            if (GUILayout.Button("Apply Configuration", GUILayout.Width(160)))
                ApplyConfiguration();

            if (GUILayout.Button("Remove Configuration", GUILayout.Width(160)))
            {
                if (EditorUtility.DisplayDialog(
                        "Remove elastic-git-storage Configuration",
                        "This will unset all elastic-git-storage git config entries.\n\nAre you sure?",
                        "Remove",
                        "Cancel"))
                {
                    RemoveConfiguration();
                }
            }

            EditorGUILayout.EndHorizontal();
        }

        // =================================================================
        // Section 4: Validation
        // =================================================================

        void DrawValidationSection()
        {
            EditorGUILayout.LabelField("Validation", EditorStyles.boldLabel);

            if (GUILayout.Button("Validate", GUILayout.Width(100)))
                RunValidation();

            if (_validationResults != null)
            {
                foreach (string result in _validationResults)
                {
                    MessageType msgType;
                    if (result.StartsWith("[PASS]"))
                        msgType = MessageType.Info;
                    else if (result.StartsWith("[WARN]"))
                        msgType = MessageType.Warning;
                    else
                        msgType = MessageType.Error;

                    EditorGUILayout.HelpBox(result, msgType);
                }
            }
        }

        // -----------------------------------------------------------------
        // Helpers — Status
        // -----------------------------------------------------------------

        void RefreshStatus()
        {
            string binFolder = GetPackageBinPath();
            _binaryPath = GitConfigHelper.FindBinary(binFolder);
            _binaryVersion = GitConfigHelper.GetBinaryVersion(_binaryPath);
            _repoRoot = GitConfigHelper.GetRepoRoot();
        }

        // -----------------------------------------------------------------
        // Helpers — Install
        // -----------------------------------------------------------------

        void FetchLatestRelease()
        {
            _isBusy = true;
            _installStatus = "Fetching release info...";

            var request = UnityWebRequest.Get(k_LatestReleaseApi);
            request.SetRequestHeader("User-Agent", "Unity-ElasticGitStorage");
            request.SetRequestHeader("Accept", "application/vnd.github+json");

            var op = request.SendWebRequest();
            op.completed += _ =>
            {
                try
                {
                    if (request.result != UnityWebRequest.Result.Success)
                    {
                        _installStatus = $"Error: {request.error}";
                        return;
                    }

                    var release = JsonUtility.FromJson<GitHubRelease>(request.downloadHandler.text);
                    _latestTag = release.tag_name ?? "";

                    // Find the matching platform asset.
                    string assetName = GitConfigHelper.GetPlatformAssetName();
                    _latestAssetUrl = null;

                    if (release.assets != null && assetName != null)
                    {
                        foreach (var asset in release.assets)
                        {
                            if (string.Equals(asset.name, assetName, StringComparison.OrdinalIgnoreCase))
                            {
                                _latestAssetUrl = asset.browser_download_url;
                                break;
                            }
                        }
                    }

                    EditorPrefs.SetString(k_EditorPrefLatestTag, _latestTag);
                    EditorPrefs.SetString(k_EditorPrefLatestUrl, _latestAssetUrl ?? "");

                    _installStatus = _latestAssetUrl != null
                        ? $"Found release {_latestTag} with asset for this platform."
                        : $"Release {_latestTag} found but no asset for {assetName ?? "unknown platform"}.";
                }
                finally
                {
                    _isBusy = false;
                    request.Dispose();
                    Repaint();
                }
            };
        }

        void StartDownload()
        {
            if (string.IsNullOrEmpty(_latestAssetUrl))
                return;

            AbortActiveRequest();

            _isBusy = true;
            _downloadProgress = 0f;
            _installStatus = "Downloading...";

            _activeRequest = UnityWebRequest.Get(_latestAssetUrl);
            _activeRequest.SetRequestHeader("User-Agent", "Unity-ElasticGitStorage");
            _activeRequest.SendWebRequest();
        }

        void OnDownloadCompleted()
        {
            var request = _activeRequest;
            _activeRequest = null;

            try
            {
                if (request.result != UnityWebRequest.Result.Success)
                {
                    _installStatus = $"Error: {request.error}";
                    return;
                }

                _installStatus = "Extracting...";

                string binFolder = GetPackageBinPath();
                if (!Directory.Exists(binFolder))
                    Directory.CreateDirectory(binFolder);

                byte[] zipData = request.downloadHandler.data;
                using (var stream = new MemoryStream(zipData))
                using (var archive = new ZipArchive(stream, ZipArchiveMode.Read))
                {
                    foreach (var entry in archive.Entries)
                    {
                        if (string.IsNullOrEmpty(entry.Name))
                            continue; // skip directory entries

                        string destPath = Path.Combine(binFolder, entry.Name);
                        using (var entryStream = entry.Open())
                        using (var fileStream = new FileStream(destPath, FileMode.Create, FileAccess.Write))
                        {
                            entryStream.CopyTo(fileStream);
                        }

                        // On macOS/Linux, make the binary executable.
                        if (Application.platform != RuntimePlatform.WindowsEditor)
                        {
                            SetExecutablePermission(destPath);
                        }
                    }
                }

                _installStatus = "Installed successfully!";
                RefreshStatus();
            }
            catch (Exception ex)
            {
                _installStatus = $"Error: {ex.Message}";
                Debug.LogException(ex);
            }
            finally
            {
                _isBusy = false;
                request.Dispose();
                Repaint();
            }
        }

        void AbortActiveRequest()
        {
            if (_activeRequest != null)
            {
                _activeRequest.Abort();
                _activeRequest.Dispose();
                _activeRequest = null;
                _isBusy = false;
            }
        }

        static void SetExecutablePermission(string filePath)
        {
            try
            {
                GitConfigHelper.RunProcess("chmod", $"+x \"{filePath}\"", Path.GetDirectoryName(filePath), 5000);
            }
            catch
            {
                // Best effort — chmod may not be available in all environments.
            }
        }

        // -----------------------------------------------------------------
        // Helpers — Configuration
        // -----------------------------------------------------------------

        void RefreshConfigDisplay()
        {
            _cfgPathValue     = GitConfigHelper.GetConfig(k_CfgPath)     ?? "(not set)";
            _cfgArgsValue     = GitConfigHelper.GetConfig(k_CfgArgs)     ?? "(not set)";
            _cfgAgentValue    = GitConfigHelper.GetConfig(k_CfgAgent)    ?? "(not set)";
            _cfgPullMainValue = GitConfigHelper.GetConfig(k_CfgPullMain) ?? "(not set)";
            _cfgPushMainValue = GitConfigHelper.GetConfig(k_CfgPushMain) ?? "(not set)";
            _cfgWriteAllValue = GitConfigHelper.GetConfig(k_CfgWriteAll) ?? "(not set)";
        }

        void ApplyConfiguration()
        {
            if (string.IsNullOrEmpty(_storagePath))
            {
                EditorUtility.DisplayDialog("Error", "Storage Path is required.", "OK");
                return;
            }

            // Determine the binary path to write into git config.
            string binFolder = GetPackageBinPath();
            string binaryPath = GitConfigHelper.FindBinary(binFolder);
            if (string.IsNullOrEmpty(binaryPath))
            {
                if (!EditorUtility.DisplayDialog(
                        "Binary Not Found",
                        "The elastic-git-storage binary was not found. Configuration will " +
                        "be written but transfers will fail until the binary is installed.\n\n" +
                        "Continue anyway?",
                        "Continue",
                        "Cancel"))
                {
                    return;
                }

                binaryPath = "elastic-git-storage"; // fallback — assume it will be on PATH.
            }

            // Build the args string.
            string args = BuildArgs();

            // Write git config.
            GitConfigHelper.SetConfig(k_CfgPath, binaryPath);
            GitConfigHelper.SetConfig(k_CfgArgs, args);
            GitConfigHelper.SetConfig(k_CfgAgent, k_TransferName);
            GitConfigHelper.SetConfigBool(k_CfgPullMain, _pullMain);
            GitConfigHelper.SetConfigBool(k_CfgPushMain, _pushMain);
            GitConfigHelper.SetConfigBool(k_CfgWriteAll, _writeAll);

            RefreshConfigDisplay();
            EditorUtility.DisplayDialog("Success", "Git configuration applied.", "OK");
        }

        void RemoveConfiguration()
        {
            GitConfigHelper.UnsetConfig(k_CfgPath);
            GitConfigHelper.UnsetConfig(k_CfgArgs);
            GitConfigHelper.UnsetConfig(k_CfgAgent);
            GitConfigHelper.UnsetConfig(k_CfgPullMain);
            GitConfigHelper.UnsetConfig(k_CfgPushMain);
            GitConfigHelper.UnsetConfig(k_CfgWriteAll);

            RefreshConfigDisplay();
            EditorUtility.DisplayDialog("Removed", "All elastic-git-storage git config entries have been removed.", "OK");
        }

        /// <summary>
        /// Builds the value for <c>lfs.customtransfer.elastic-git-storage.args</c>.
        ///
        /// The Go binary uses cobra for argument parsing. Boolean flags
        /// (pullmain, pushmain, writeall) are written as separate git config
        /// booleans instead of being crammed into the args string. Only the
        /// storage path (with optional compression prefix) and optional
        /// --pushdir are placed in the args string.
        /// </summary>
        string BuildArgs()
        {
            // The compression prefix is embedded INSIDE the basedir string,
            // parsed by splitBaseDirs() in service.go:
            //   "--compression=lz4 D:/LFS/Storage"
            string compression = k_CompressionOptions[_compressionIndex];
            string basedir = _storagePath;

            // Wrap paths with spaces in single quotes (Go code strips them).
            if (basedir.Contains(" "))
                basedir = $"'{basedir}'";

            if (compression != "none")
                basedir = $"--compression={compression} {basedir}";

            string args = "";

            // Optional --pushdir
            if (!string.IsNullOrEmpty(_pushPath))
            {
                string pushPathArg = _pushPath;
                if (pushPathArg.Contains(" "))
                    pushPathArg = $"'{pushPathArg}'";

                args += $"--pushdir {pushPathArg} ";
            }

            args += basedir;

            return args;
        }

        // -----------------------------------------------------------------
        // Helpers — Validation
        // -----------------------------------------------------------------

        void RunValidation()
        {
            var results = new System.Collections.Generic.List<string>();

            // 1. Binary found
            string binFolder = GetPackageBinPath();
            string binary = GitConfigHelper.FindBinary(binFolder);
            if (!string.IsNullOrEmpty(binary))
                results.Add($"[PASS] Binary found: {binary}");
            else
                results.Add("[FAIL] Binary not found (not in Bin~ folder or PATH)");

            // 2. Git LFS installed
            var (lfsInstalled, lfsVersion) = GitConfigHelper.CheckGitLfs();
            if (lfsInstalled)
                results.Add($"[PASS] Git LFS installed: {lfsVersion}");
            else
                results.Add("[FAIL] Git LFS not installed or not on PATH");

            // 3. Storage path
            string storageCfg = GitConfigHelper.GetConfig(k_CfgArgs);
            if (!string.IsNullOrEmpty(storageCfg))
            {
                // Try to extract the storage path from the args.
                string extractedPath = ExtractStoragePath(storageCfg);
                if (!string.IsNullOrEmpty(extractedPath))
                {
                    bool isRclone = extractedPath.Contains(":");
                    if (isRclone)
                    {
                        results.Add($"[PASS] Storage path is an rclone remote: {extractedPath}");
                    }
                    else if (Directory.Exists(extractedPath))
                    {
                        results.Add($"[PASS] Storage path exists: {extractedPath}");
                    }
                    else
                    {
                        results.Add($"[FAIL] Storage path does not exist: {extractedPath}");
                    }
                }
                else
                {
                    results.Add("[WARN] Could not parse storage path from args config");
                }
            }
            else
            {
                results.Add("[FAIL] Storage path not set (lfs.customtransfer.elastic-git-storage.args is empty)");
            }

            // 4. Push path
            string pushDirCfg = ExtractPushDir(storageCfg);
            if (!string.IsNullOrEmpty(pushDirCfg))
            {
                bool isRclone = pushDirCfg.Contains(":");
                if (isRclone)
                    results.Add($"[PASS] Push path is an rclone remote: {pushDirCfg}");
                else if (Directory.Exists(pushDirCfg))
                    results.Add($"[PASS] Push path exists: {pushDirCfg}");
                else
                    results.Add($"[FAIL] Push path does not exist: {pushDirCfg}");
            }
            else
            {
                results.Add("[WARN] Push path not set (will use storage path)");
            }

            // 5. Git config keys
            string cfgPath = GitConfigHelper.GetConfig(k_CfgPath);
            string cfgAgent = GitConfigHelper.GetConfig(k_CfgAgent);

            if (!string.IsNullOrEmpty(cfgPath) && !string.IsNullOrEmpty(cfgAgent) && !string.IsNullOrEmpty(storageCfg))
                results.Add("[PASS] Git config keys are set");
            else
            {
                string missing = "";
                if (string.IsNullOrEmpty(cfgPath))   missing += " path";
                if (string.IsNullOrEmpty(cfgAgent))  missing += " standalonetransferagent";
                if (string.IsNullOrEmpty(storageCfg)) missing += " args";
                results.Add($"[FAIL] Missing git config keys:{missing}");
            }

            _validationResults = results.ToArray();
        }

        /// <summary>
        /// Extracts the storage path from the args string. The storage path
        /// is the last positional token after any cobra flags.
        /// </summary>
        static string ExtractStoragePath(string args)
        {
            if (string.IsNullOrEmpty(args))
                return null;

            // Strip known cobra flags to isolate the basedir.
            string remaining = args;

            // Remove --pushdir {value}
            int pushIdx = remaining.IndexOf("--pushdir", StringComparison.Ordinal);
            if (pushIdx >= 0)
            {
                // Find the value after --pushdir (may be quoted).
                string afterFlag = remaining.Substring(pushIdx + "--pushdir".Length).TrimStart();
                string pushValue = ConsumeToken(afterFlag, out string rest);
                remaining = remaining.Substring(0, pushIdx) + rest;
            }

            remaining = remaining.Trim();

            // Strip compression prefix if present.
            if (remaining.StartsWith("--compression="))
            {
                int space = remaining.IndexOf(' ');
                remaining = space >= 0 ? remaining.Substring(space + 1).Trim() : "";
            }

            // Strip single quotes.
            remaining = remaining.Trim('\'', ' ');

            return string.IsNullOrEmpty(remaining) ? null : remaining;
        }

        /// <summary>
        /// Extracts the --pushdir value from the args string.
        /// </summary>
        static string ExtractPushDir(string args)
        {
            if (string.IsNullOrEmpty(args))
                return null;

            int idx = args.IndexOf("--pushdir", StringComparison.Ordinal);
            if (idx < 0)
                return null;

            string afterFlag = args.Substring(idx + "--pushdir".Length).TrimStart();
            string value = ConsumeToken(afterFlag, out _);
            return string.IsNullOrEmpty(value) ? null : value.Trim('\'');
        }

        /// <summary>
        /// Consumes the first whitespace-delimited token from a string,
        /// respecting single-quoted spans. Returns the token and outputs
        /// the remainder.
        /// </summary>
        static string ConsumeToken(string input, out string remainder)
        {
            input = input.TrimStart();
            if (string.IsNullOrEmpty(input))
            {
                remainder = "";
                return null;
            }

            if (input[0] == '\'')
            {
                int close = input.IndexOf('\'', 1);
                if (close > 0)
                {
                    remainder = input.Substring(close + 1);
                    return input.Substring(1, close - 1);
                }
            }

            int space = input.IndexOf(' ');
            if (space < 0)
            {
                remainder = "";
                return input;
            }

            remainder = input.Substring(space + 1);
            return input.Substring(0, space);
        }

        // -----------------------------------------------------------------
        // Helpers — GUI
        // -----------------------------------------------------------------

        static void DrawSelectableConfigRow(string label, string value)
        {
            EditorGUILayout.BeginHorizontal();
            EditorGUILayout.PrefixLabel(label);
            EditorGUILayout.SelectableLabel(
                value ?? "(not set)",
                EditorStyles.textField,
                GUILayout.Height(EditorGUIUtility.singleLineHeight));
            EditorGUILayout.EndHorizontal();
        }

        // -----------------------------------------------------------------
        // Helpers — Package paths
        // -----------------------------------------------------------------

        /// <summary>
        /// Returns the absolute path to the <c>Editor/Bin~/</c> folder
        /// inside this package.
        /// </summary>
        string GetPackageBinPath()
        {
            // MonoScript.FromScriptableObject works on EditorWindows.
            var script = MonoScript.FromScriptableObject(this);
            if (script == null)
                return null;

            string scriptPath = AssetDatabase.GetAssetPath(script);
            if (string.IsNullOrEmpty(scriptPath))
                return null;

            // scriptPath is e.g. "Packages/com.frostebite.elastic-git-storage/Editor/ElasticGitStorageWindow.cs"
            // or "Assets/.../Editor/ElasticGitStorageWindow.cs" depending on how the package is referenced.
            string editorDir = Path.GetDirectoryName(scriptPath);
            string binRelative = Path.Combine(editorDir, "Bin~");
            return Path.GetFullPath(binRelative);
        }

        // -----------------------------------------------------------------
        // JSON DTOs for GitHub Releases API (minimal)
        // -----------------------------------------------------------------

        [Serializable]
        class GitHubRelease
        {
            public string tag_name;
            public GitHubAsset[] assets;
        }

        [Serializable]
        class GitHubAsset
        {
            public string name;
            public string browser_download_url;
        }
    }
}
