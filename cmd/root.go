// Copyright © 2018 Steve Streeting
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package cmd

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/sinbad/lfs-folderstore/service"
	"github.com/sinbad/lfs-folderstore/util"
	"github.com/spf13/cobra"
)

var (
	baseDir      string
	pushDir      string
	useAction    bool // deprecated: enables both pull and push actions
	pullMain     bool
	pushMain     bool
	printVersion bool
)

// RootCmd represents the base command when called without any subcommands
var RootCmd *cobra.Command

// Execute adds all child commands to the root command sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
}

func init() {
	RootCmd = &cobra.Command{
		Use:   "lfs-folderstore",
		Short: "git-lfs custom transfer adapter to store all data in a folder",
		Long: `lfs-folderstore treats a simple folder, probably a shared one,
		as the remote store for all LFS object data. Upload and download functions
		are turned into simple file copies to destinations determined by the id
		of the object.`,
		Run: rootCommand,
	}

	RootCmd.Flags().StringVarP(&baseDir, "basedir", "d", "", "Base directory for all file operations")
	RootCmd.Flags().StringVarP(&pushDir, "pushdir", "p", "", "Optional base directory for uploads; defaults to basedir")
	RootCmd.Flags().BoolVar(&useAction, "useaction", false, "Also use LFS-provided actions for transfers (deprecated)")
	RootCmd.Flags().BoolVar(&pullMain, "pullmain", false, "Allow fallback pulling from main LFS remote")
	RootCmd.Flags().BoolVar(&pushMain, "pushmain", false, "Also push to main LFS remote")
	RootCmd.Flags().BoolVarP(&printVersion, "version", "", false, "Print version")
	RootCmd.SetUsageFunc(usageCommand)

}

func usageCommand(cmd *cobra.Command) error {
	usage := `
Usage:
  lfs-folderstore [options] <basedir>

Arguments:
  basedir      Base directory for the object store (required)

Options:
  --pushdir    Optional base directory for uploads; defaults to basedir
  --useaction  Also perform transfers using LFS-provided actions (deprecated)
  --pullmain   Allow fallback pulling from main LFS remote
  --pushmain   Also push to main LFS remote
  --version    Report the version number and exit

Note:
  This tool should only be called by git-lfs as documented in Custom Transfers:
  https://github.com/git-lfs/git-lfs/blob/master/docs/custom-transfers.md

  The arguments should be provided via gitconfig at lfs.customtransfer.<name>.args
`
	fmt.Fprint(os.Stderr, usage)
	return nil
}

func rootCommand(cmd *cobra.Command, args []string) {

	if printVersion {
		os.Stderr.WriteString(fmt.Sprintf("lfs-folder %v\n", Version))
		os.Exit(0)
	}

	// pull directory: flag > arg > git config
	pullDir := strings.TrimSpace(baseDir)
	if pullDir == "" && len(args) > 0 {
		pullDir = strings.TrimSpace(args[0])
	}
	if pullDir == "" {
		pullDir = strings.TrimSpace(getGitConfig("lfs.folderstore.pull"))
	}
	if pullDir == "" {
		os.Stderr.WriteString("Required: base directory (use --basedir or git config lfs.folderstore.pull)\n")
		cmd.Usage()
		os.Exit(1)
	}
	if !isRclonePath(pullDir) {
		stat, err := os.Stat(pullDir)
		if err != nil || !stat.IsDir() {
			os.Stderr.WriteString(fmt.Sprintf("%q does not exist or is not a directory", pullDir))
			cmd.Usage()
			os.Exit(3)
		}
	}

	// push directory: flag > git config > pullDir
	push := strings.TrimSpace(pushDir)
	if push == "" {
		push = strings.TrimSpace(getGitConfig("lfs.folderstore.push"))
	}
	if push == "" {
		push = pullDir
	}
	if !isRclonePath(push) {
		stat, err := os.Stat(push)
		if err != nil || !stat.IsDir() {
			os.Stderr.WriteString(fmt.Sprintf("%q does not exist or is not a directory", push))
			cmd.Usage()
			os.Exit(3)
		}
	}

	// action flags: command line overrides config
	if useAction {
		pullMain = true
		pushMain = true
	} else {
		if !pullMain {
			if b, ok := getGitConfigBool("lfs.folderstore.pullmain"); ok {
				pullMain = b
			}
		}
		if !pushMain {
			if b, ok := getGitConfigBool("lfs.folderstore.pushmain"); ok {
				pushMain = b
			}
		}
	}

	service.Serve(pullDir, push, pullMain, pushMain, os.Stdin, os.Stdout, os.Stderr)
}

func isRclonePath(path string) bool {
	if runtime.GOOS == "windows" {
		if len(path) >= 2 && path[1] == ':' {
			return false
		}
	}
	return strings.Contains(path, ":")
}

func getGitConfig(key string) string {
	cmd := util.NewCmd("git", "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getGitConfigBool(key string) (bool, bool) {
	cmd := util.NewCmd("git", "config", "--bool", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return false, false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(string(out)))
	if err != nil {
		return false, false
	}
	return b, true
}
