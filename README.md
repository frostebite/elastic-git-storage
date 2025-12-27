<img width="1536" height="1024" alt="image" src="https://github.com/user-attachments/assets/a3d23132-f275-4843-9ade-f3a6decd4f88" />



# elastic-git-storage
A git-lfs plugin that allows you to conveniently use any popular storage providers (Google Drive, AWS, Azure, local drive) alongside normal git providers like GitHub by storing "LFS" large files in a configurable storage location
 - Store any of your LFS files in any storage provider rclone supports https://rclone.org/overview/
 - Whether you're a big team or a small team, the flexibility and unbeatable prices this flexibility enables can liberate your projects from costly providers.
 - Great for game development or any project that uses large files.


> Forked from [lfs-folderstore](https://github.com/sinbad/lfs-folderstore) by Steve Streeting.

## Batteries included
Fast and easy to setup. 

## What is it?

a [Custom Transfer
Agent](https://github.com/git-lfs/git-lfs/blob/master/docs/custom-transfers.md)
for [Git LFS](https://git-lfs.github.com/) which allows you to use a plain
folder, scripted push/pull or any RClone storage as the remote storage location for all your large media files.

## Why?

Let's say you use Git, but you don't use any fancy hosting solution. You just
use a plain Git repo on a server somewhere, perhaps using SSH so you don't even
need a web server. It's simple and great.

But how do you use Git LFS? It usually wants a server to expose API endpoints.
Sure you could use one of the [big](https://bitbucket.org) [hosting](https://github.com)
[providers](https://gitlab.com), but that makes everything more complicated.

Maybe you already have plenty of storage sitting on a NAS somewhere, or via
Dropbox, Google Drive etc, which you can share with your colleagues. Why not just
use that?

So that's what this adapter does. When enabled, all LFS uploads and downloads
are simply translated into file copies to/from a folder that's visible to your
system already. Put your media on a shared folder, or on a synced folder like
Dropbox, or Synology Cloud Drive etc.

## How to use

### Prerequisites

You need to be running Git LFS version 2.3.0 or later.

### Download &amp; install

You will need `elastic-git-storage[.exe]` to be on your system path somewhere.

Either download and extract the [latest
release](https://github.com/frostebite/elastic-git-storage/releases), or build it from
source using the standard `go build`.

### Configure a fresh repo

Starting a new repository is the easiest case.

* Initialise your repository as usual with `git init` and `git lfs track *.png` etc
* Create some commits with LFS binaries
* Add your plain git remote using `git remote add origin <url>`
* Run these commands to configure your LFS folder:
  * `git config --add lfs.customtransfer.elastic-git-storage.path elastic-git-storage`
  * `git config --add lfs.customtransfer.elastic-git-storage.args "C:/path/to/your/folder"`
  * `git config --add lfs.standalonetransferagent elastic-git-storage`
* `git push origin master` will now copy any media to that folder

A few things to note:

* As shown, if on Windows, use forward slashes for path separators
* If you have spaces in your path, add **additional single quotes** around the path
    * e.g. `git config --add lfs.customtransfer.elastic-git-storage.args "'C:/path with spaces/folder'"`
* The `standalonetransferagent` forced Git LFS to use the folder agent for all
  pushes and pulls. If you want to use another remote which uses the standard
  LFS API, you should see the next section.

### Configure an existing repo

If you already have a Git LFS repository pushing to a standard LFS server, and
you want to either move to a folder, or replicate, it's a little more complicated.

* Create a new remote using `git remote add folderremote <url>`. Do this even if you want to keep the git repo at the same URL as currently.
* Run these commands to configure the folder store:
  * `git config --add lfs.customtransfer.elastic-git-storage.path elastic-git-storage`
  * `git config --add lfs.customtransfer.elastic-git-storage.args "C:/path/to/your/folder"`
  * `git config --add lfs.<url>.standalonetransferagent elastic-git-storage` - important: use the new Git repo URL
* `git push folderremote master ...` - important: list all branches you wish to keep LFS content for. Only LFS content which is reachable from the branches you list (at any version) will be copied to the remote

### Cloning a repo

There is one downside to this 'simple' approach to LFS storage - on cloning a
repository, git-lfs can't know how to fetch the LFS content, until you configure
things again using `git config`. That's the nature of the fact that you're using
a simple Git remote with no LFS API to expose this information.

It's not that hard to resolve though, you just need a couple of extra steps
when you clone fresh. Here's the sequence:

* `git clone <url> <folder>`
    * this will work for the git data, but will report "Error downloading object" when trying to get LFS data
* `cd <folder>` - to enter your newly cloned repo
* Configure as with a new repo:
  * `git config --add lfs.customtransfer.elastic-git-storage.path elastic-git-storage`
  * `git config --add lfs.customtransfer.elastic-git-storage.args "C:/path/to/your/folder"`
  * `git config --add lfs.standalonetransferagent elastic-git-storage`
* `git reset --hard master`
  * This will sort out the LFS files in your checkout and copy the content from the now-configured shared folder

### Command-line usage and flag precedence

You can run the binary directly (Git LFS does this under the hood). Flags mirror the tool's usage output:

```
Usage:
  elastic-git-storage [options] <basedir>

Arguments:
  basedir      Base directory for the object store (required unless provided via config)

Options:
  --basedir, -d   Base directory for downloads; overrides positional arg and git config
  --pushdir, -p   Optional base directory for uploads; defaults to basedir if omitted
  --useaction     Also perform transfers using LFS-provided actions (deprecated)
  --pullmain      Allow fallback pulling from main LFS remote
  --pushmain      Also push to main LFS remote
  --version       Report the version number and exit

Notes:
  - Pull path precedence: --basedir flag > positional argument > git config lfs.folderstore.pull
  - Push path precedence: --pushdir flag > git config lfs.folderstore.push > resolved pull path
  - Main-remote fallbacks: flags override git config lfs.folderstore.pullmain / lfs.folderstore.pushmain
  - Custom transfer arguments are normally set via git config at lfs.customtransfer.<name>.args
```

## Notes

* The shared folder is, to git, still a "remote" and so separate from clones. It
  only interacts with it during `fetch`, `pull` and `push`.
* Copies are used in all cases, even if you're using Dropbox, Google Drive etc
  as your folder store. While hard links are possible and would save space, for
  integrity reasons (no copy-on-write) I've kept things simple.
* It's entirely up to you whether you use different folder paths per project, or
  share one between many projects. In the former case, it's easier to reclaim
  space by deleting a specific project, in the latter case you can save space if
  you have common files between projects (they'll have the same hash)

## Advanced capabilities

These features extend the basic folder store and can be combined as needed.

### Multiple storage locations
Provide several folder paths separated by semicolons in the configuration argument. Each
location is searched in order until the object is found.

```bash
git config --add lfs.customtransfer.elastic-git-storage.args \
  "D:/fast-cache;/mnt/slow-storage"
```

### Scripted transfers
Prefix a location with `|` to run a shell script instead of using a directory. The script
receives environment variables such as `OID`, `DEST` (for pulls), `FROM` (for pushes) and
`SIZE`, allowing custom transfer logic and prioritisation.

```bash
git config --add lfs.customtransfer.elastic-git-storage.args "|./transfer.sh;/mnt/storage"
```

`transfer.sh` can read `$OID` to locate the object and copy it to `$DEST` or from `$FROM`.

### Configurable compression
Compression is not automatic. Specify the desired compression for each storage
location via Git config. Supported formats are `zip`, `lz4`, or `none`.

```bash
git config --add lfs.customtransfer.elastic-git-storage.args "--compression=zip /mnt/storage"
```

Objects will be compressed on upload and decompressed on download according to the
configured mode.

### rclone integration
Paths prefixed with an [rclone](https://rclone.org) alias (e.g. `remote:path`) are resolved
via `rclone`, enabling uploads to or downloads from any backend that rclone supports.

```bash
git config --add lfs.customtransfer.elastic-git-storage.args "remote:bucket/path"
```

### Mirroring to a main LFS server
Use the `--pullmain` flag to fall back to the standard LFS server for downloads. Combine
with `--pushmain` to mirror uploads there too. The older `--useaction` flag still enables
both for backwards compatibility.

```bash
git config --add lfs.customtransfer.elastic-git-storage.args \
  "--pullmain --pushmain /mnt/lfs-folder"
```

### Separate upload destinations
Override the upload location separately from downloads with the `--pushdir` flag, which
may point to another folder or rclone remote.

```bash
git config --add lfs.customtransfer.elastic-git-storage.args \
  "--pushdir /mnt/upload /mnt/download"
```

### Git configuration
Base directories and main-remote options may also be configured via git config keys
`lfs.folderstore.pull`, `lfs.folderstore.push`, `lfs.folderstore.pullmain` and
`lfs.folderstore.pushmain` which can be set globally or per-repo.

```bash
git config --global lfs.folderstore.pull /mnt/storage
git config --global lfs.folderstore.push /mnt/uploads
git config --global lfs.folderstore.pullmain true
git config --global lfs.folderstore.pushmain true
```

These settings remove the need to pass arguments in `lfs.customtransfer.elastic-git-storage.args`.

## License

This project is licensed under the [MIT License](LICENSE).
