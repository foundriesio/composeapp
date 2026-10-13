# Installing app images into dockerd's containerd

*Design note, 2026-10-08, revised 2026-10-09 after the phase 0 checks and the second
Astra review, and 2026-10-12 with the device result for the minimum Docker version.
Implemented on this branch; the code review record is in the pull request.*

## Decision

On a dockerd that uses the containerd image store, `composectl install` streams each
service image from composectl's store into dockerd's containerd under a short install
lease, unpacks it, and creates the image records. Deleting the lease then releases
the compressed layers, and containerd's GC removes them unless something else, such
as an image pulled with `docker pull`, still references them. The only persistent
compressed copy of an app image is then the one in composectl's store. On a
graphdriver dockerd, and on a Docker older than the qualified minimum, install keeps
using `docker load`.

Pull, the store layout, status, remove, uninstall, the update states and the
aktualizr-lite interface stay as they are.

| | Compressed copies kept | Unpacked |
|---|---|---|
| Today, containerd image store | 2 (composectl store and containerd content store) | 1 |
| This plan | 1 (composectl store) | 1 |

On the 380 MB test image this goes from about 1853 MB to about 1473 MB. The extra
compressed copy during install is temporary.

## Why this route

- **The store copy keeps the bytes for a repair local.** In the power-cut
  experiments (Exp P), a cut up to about 35 s after a pull left snapshots committed
  but empty, and a re-pull did not repair them. With the store copy, reinstalling
  needs no network. The bytes alone do not repair a broken snapshot: see "Crash
  recovery" for what does.
- **aktualizr-lite behaves the same as today.** After a store prune it calls
  `/images/prune` with `dangling=false`, which removes images no container uses.
  `check --install` then reports the image missing, and aktualizr-lite reinstalls it
  from the store. There is no durable lease that the prune would have to work around.
- **Install skips layers that are already unpacked.** Unpack stats each chain ID
  before it applies the layer, so base layers shared with the running version are
  not streamed again. Today `docker load` sends every blob. The stat checks only that
  the snapshot exists, not that its files are intact; "Crash recovery" says why this
  is safe for snapshots composectl creates.
- **The registry path does not change.** composectl's resolver, the token proxy,
  resume and the split between fetch and install all stay as they are.

### Options not taken now

- **Link-in** (hardlink the store blob into the content root): phase 2. Build it only
  if device measurements show that the write volume or the install peak matters. Try
  the link and fall back to streaming on any error, because a hardlink fails across
  mounts. Read the blob back after registration, because GC can remove the linked
  file before commit.
- **Pull into containerd and discard** (the 2026-10-08 brief): phase 3. Build it only
  if the persistent compressed copy is shown to be the limit. Its retention model
  (durable leases on snapshots, three predicates for status, a reconcile step) is
  much larger. The Astra review records its blockers.
- **`docker pull` through the Docker API**: rejected. It loses the token proxy auth
  path and the split between fetch and install.
- **`docker load` of an OCI layout followed by stripping layer labels**: this was the
  fallback if new `go.mod` modules were refused. It is not needed now. Keeping the
  pinned digest through this path is untested.

## Install sequence (containerd image store)

composectl takes an exclusive lock on `<StoreRoot>/.install.lock` (`syscall.Flock`)
for every operation that writes to containerd or deletes from it: install,
uninstall, `rm` and `prune`. A second composectl waits for the lock. Concurrent
installs are not supported, so nothing more is needed.

Under the lock, for each app:

1. Delete every lease whose ID starts with `composectl/install/`, synchronously.
   Under the lock no other install is running, so any such lease was left by an
   interrupted install. This also releases the snapshots that install created
   (see "Crash recovery"). Then create the lease `composectl/install/<app>@<digest>`
   with no expiry.
2. For each image, in manifest order, find the first layer whose chain ID has no
   snapshot. Stream that layer and every layer after it from the store with
   `content.WriteBlob` under the lease. Also stream the config and the manifest, and
   the index when the image is pinned by one.
3. Write the manifest without `gc.ref.content.l.N` labels, so that the layers are
   held only by the lease. Keep the labels for the config, and on an index keep
   `gc.ref.content.m.N` pointing at the platform manifest. `WriteBlob` does not
   change the labels of a manifest that already exists, so a manifest written
   earlier by `docker pull` keeps its layers (see "Accepted consequences").
4. Run `Image.Unpack` on the snapshotter that `/info` reports. If it fails because
   a layer blob is missing (a base snapshot was removed between step 2 and step 4,
   for example by `docker image prune`), stream every layer of that image and run
   `Unpack` once more.
5. Call `syncfs` on the snapshotter root, once per app. Required: see check 6.
6. Create or update the image records: the pinned digest ref, and the `GetTagRef`
   tag that `ps` and status read today. Use `Create`, and `Update` of the target
   when the record already exists, so that a retry is idempotent.
7. Report each installed image through the existing progress callback with
   `ImageLoadStateImageLoaded`, so that `LoadedImages` and `update cancel` keep
   working.
8. Delete the lease synchronously. containerd's GC then removes the compressed
   layers that nothing else references.

The lease has no expiry. containerd measures `gc.expire` by wall clock, and boards
without a real-time clock can jump forward by months when NTP syncs, which could
expire the lease in the middle of an install. Orphan leases are removed in step 1
of the next install and by `composectl prune`.

## Crash recovery

The rule: a snapshot that a composectl image record references is durable, because
step 5 runs `syncfs` before step 6 creates the records. A snapshot created after
the last `syncfs` can only be referenced through the install lease.

So after an interrupted install:

- Snapshots created by the interrupted install are held only by its lease. Step 1
  of the next install deletes that lease synchronously, and GC removes them before
  step 2 looks for existing snapshots. A broken snapshot is not reused.
- Records the interrupted install already created point at synced snapshots. The
  retry updates them (step 6).
- If the crash came during lease deletion, the lease may be gone with GC not
  finished. The records protect everything that must stay. The layer records are
  removed before the files are unlinked, so a cut in between leaves orphan files
  that containerd removes at its next content cleanup (see the 2026-10-13 results).

Out of scope, and documented as unsupported on devices:

- Snapshots created by something other than composectl, such as `docker pull` or
  `docker build`, that a crash left broken. composectl cannot tell them from intact
  ones, and step 2 would reuse them. The manual repair is to remove the images that
  use them and run `docker image prune`.
- A snapshotter whose GC fails to remove a broken snapshot. Exp P saw this with
  erofs. Qualifying erofs is a deployment gate (see "Still unproven").

## Discovery and host support

Docker's `/info` is read with a raw GET through the transport of the existing
Docker client. Docker v25.0.3 has no `Containerd` field, and this avoids bumping the
Docker module. composectl uses these fields:

- `Containerd.Address` and `Containerd.Namespaces.Containers`;
- `Driver`, the snapshotter name, when `DriverStatus` has the entry
  `driver-type` = `io.containerd.snapshotter.v1`. This replaces today's `overlayfs`
  hardcode and `DriverStatus[0][1]` in `adjustOptionsIfContainerd`;
- `ServerVersion`.

composectl uses the containerd install only when all of these hold; otherwise it
uses `docker load`, unchanged:

- the daemon uses the containerd image store (the `driver-type` entry is present);
- the Docker host is a local unix socket, because composectl connects to the
  containerd socket and reads the snapshotter root on the same host;
- the server version is at least the qualified minimum. Docker 25 leaves images
  whose layers are missing out of `docker image ls`
  (`daemon/containerd/image_list.go`), and `ps` and `check --install` read that
  list. Docker 29.7.2 lists them (checked, see the results). The minimum is set to
  the oldest version on which the listing is checked: 29.5.2, checked on the board
  (see "Results on the device, 2026-10-12"). The version string may carry a `v`
  prefix (`v29.5.2` on the Arduino build of Docker); the comparison strips it.

This covers stock Docker and the Foundries-patched dockerd; the containerd route
uses only stock APIs and does not depend on the patch.

The containerd socket is normally writable only by root. If composectl cannot
connect to it on a daemon that qualifies, it fails with an error that says so. It
does not fall back to `docker load`, because that would quietly bring back the
second compressed copy.

## Storage precheck

`checkApps` (`cmd/composectl/cmd/check.go`) checks the total size against
`StoreRoot` only. The new precheck checks each filesystem against its own needs:

| Term | Bytes | Path |
|---|---|---|
| store | missing blobs, aligned to the block size | `StoreRoot` |
| bundle | the extracted app bundle | `ComposeRoot` |
| install peak | compressed bytes of the layers that have to be streamed | containerd content root |
| runtime | unpacked size of the missing snapshots | snapshotter root |

The content root and the snapshotter root come from the introspection API
(`Exports["root"]` of each plugin). Terms that share an `st_dev` are added together
before they are compared with the free space. The runtime term counts each missing
snapshot once, by chain ID, even when its compressed blob is already in the store:
the same blob above a different parent is a different snapshot. The unpacked size
comes from the layers metadata, with the existing fallback when an app has none
(`pkg/compose/v1/app.go`), which is an estimate. The current watermark and
reserved-storage options apply to each filesystem. The check counts bytes only:
inodes and concurrent writers are not covered.

`check --format json` keeps its current fields and adds one entry per filesystem:

```json
"storage": [
  {"path": "/var/sota/reset-apps", "required": 412000000, "free": 5100000000, "ok": true},
  {"path": "/var/lib/containerd", "required": 1090000000, "free": 5100000000, "ok": true}
]
```

aktualizr-lite should check this list instead of using the Docker root alone. That
change is made in the aktualizr-lite repository.

On a host that uses `docker load` the current single term stays.

The calculation lives in `pkg/compose`, used by both `cmd/composectl/cmd/check.go`
and `pkg/update/init.go`. `init.go` today adds `blobStoreSize` to
`runtimeSizeTotal` (and adds it twice to `totalSize`); both should use
`blobRuntimeSize`. This fixes the reported numbers only: `init.go` does not reject
an update on size, and that stays as it is.

The erofs snapshotter may need temporary space beyond the unpacked size while it
converts a layer. That term is added when erofs is qualified.

## Code changes

| File | Change |
|---|---|
| `pkg/compose/docker_client.go` | `/info` discovery (address, namespace, snapshotter) |
| `pkg/compose/ctrd_install.go` (new) | the install sequence above, crash recovery and the install lock |
| `pkg/compose/install.go` | split `Install` (88 lines, over the 60-line limit) and pick containerd or `docker load` per daemon |
| `pkg/compose/image_loader.go` | `adjustOptionsIfContainerd` uses the discovered snapshotter |
| `pkg/compose/storage.go` (new) | the per-filesystem sizing shared by `check` and `update init` |
| `cmd/composectl/cmd/check.go` | uses it; the `storage` JSON field |
| `pkg/update/init.go` | uses it, and the runtime-size fix |
| `pkg/compose/remove.go`, `uninstall.go`, `cmd/composectl/cmd/prune.go` | take the install lock; `prune` deletes orphan install leases |
| `go.mod`, `go.sum` | the root package of the containerd v1.7.15 client is imported. This adds 17 indirect modules and makes `golang.org/x/sys` direct (list below). No Go bump: v1.7.15 declares Go 1.21. The v2 client needs Go 1.23 (v2.0, v2.1) or 1.24.3 (v2.2). |
| `test/compose/docker-compose.yml`, `test/integration/` | a stock dockerd 29 service with the containerd image store, and the tests below |

New files: the containerd install is a second implementation next to `docker load`;
putting it into `install.go` would mix the two paths. The sizing is shared by two
callers that today compute it separately, and differently.

Unchanged: fetch, status (`ps` and `check --install` read the Docker image list,
which lists these images on qualified versions), the store, the update state
machine, and `MakeAkliteHappy`.

Not emitted: images created directly in containerd produce no Docker `load` or
`pull` event. composectl does not read Docker events; whether aktualizr-lite does is
to be checked in its repository.

Modules added to `go.mod` as indirect, plus `golang.org/x/sys` moving from indirect to
direct:
`AdamKorcz/go-118-fuzz-build`, `containerd/cgroups`, `containerd/continuity`,
`containerd/fifo`, `containerd/ttrpc`, `containerd/typeurl/v2`,
`docker/go-events`, `golang/groupcache`, `google/go-cmp`, `google/uuid`,
`moby/sys/mountinfo`, `moby/sys/signal`, `opencontainers/runtime-spec`,
`opencontainers/selinux`, `go.opencensus.io`, `golang.org/x/text`,
`google.golang.org/genproto`. AGENTS.md asks for approval of new modules; this list
is that request.

## Phase 0: rig checks before code

1. The containerd v1.7.15 client against containerd 2.4.1 under dockerd 29:
   `WriteBlob` under a lease, `Unpack`, `Create` image, and synchronous lease delete.
2. dockerd lists, runs and `compose up`s the image offline, both by the pinned digest
   and by the short tag.
3. The compressed layers are gone from the content root after install, and the
   snapshots stay.
4. `/images/prune` with aktualizr-lite's filter removes the unused image, and
   reinstalling from the store works offline.
5. An update that shares base layers streams only the layers that differ.
6. Exp P repeated with `syncfs` before the records are created: no committed but
   broken snapshot after a cut.
7. On the target eMMC: bytes written and install time, compared with `docker load`.

If check 1 fails, the fallback is a newer 1.7.x client before considering v2.

### Results, 2026-10-09 (dev container)

Run in the test environment of this repository on the containerd image store with
dockerd 29.7.2 and 29.7.2+fio, both on containerd 2.4.1 with the overlayfs
snapshotter. A temporary probe test published and pulled two app versions with the
test fixtures and ran the install sequence above against dockerd's containerd. Both
daemons gave the same results.

| Check | Result |
|---|---|
| 1. v1.7.15 client against containerd 2.4.1 | Pass. `WriteBlob` under the lease, `Unpack`, image create and synchronous lease delete all work, also for an image pinned by an index. Discovery through a raw `/info` GET and the content and snapshotter roots from introspection work as described. |
| 2. Offline run | Pass. `docker image inspect` and `docker run --pull never --network none` work by the pinned digest and by the short tag; `docker compose up --pull never` of the installed bundle starts every service. |
| 3. Layers gone, snapshots kept | Pass. After install no layer blob is in the content root; the manifest, config and every snapshot in the chain are. |
| 4. Prune and offline reinstall | Pass. `/images/prune` with `dangling=false` removes the records, the snapshots and the config; reinstalling from the store streams every layer again and the app runs offline. |
| 5. Shared base layers | Pass. v2 replaces one image with an image built on top of v1's base: install streamed 1 of 3 layers. `Unpack` does not read the content of a layer whose snapshot exists. |
| 6. Power cut with `syncfs` | On the device, see below. |
| 7. eMMC writes and time | On the device, see below. In the dev container, one comparison for 8.6 MB of layers: 0.49 s for the install, 0.40 s for `docker load`. |
| Status on the discarded layers (added after the review) | Pass on 29.7.2, stock and fio. `docker image ls` lists both images with tag, digest, ID and size; `composectl check --install` reports no missing image; `composectl run` and `ps` report the app running; `stop`, `uninstall` and `rm` work. |

### Results on the device, 2026-10-09 (checks 6 and 7)

Board: Arduino UNO Q `unoq-dev`, kernel 7.0.14, ext4 on eMMC, stock dockerd 29.5.2 on
the containerd image store with containerd 2.3.3 and the overlayfs snapshotter. App:
node-red 4.1.11 (v1, 17 layers, 208 MB compressed), updated to 4.1.12 (v2, 13 new
layers, 163 MB). A probe binary ran the install sequence above against a composectl
store copied to the board, or `docker load` from the same store (the unpatched path).

**Check 7, 3 rounds, arms alternated:**

| | containerd install | `docker load` |
|---|---|---|
| v1 cold: time | 160–161 s | 153–178 s |
| v1 cold: written to eMMC | 993 MB | 992–993 MB |
| v1 → v2 update: time | 126–128 s | 123–128 s |
| v1 → v2 update: written to eMMC | 797–801 MB | 846–849 MB |
| Disk used with v1 and v2 installed | 1244 MiB | 1602 MiB |

Cold installs write the same and take about the same time. The update writes 45–52 MB
less with the containerd install in all three rounds, about 6 %. Three rounds per
arm are not enough to call the times equal; per-stage times were not recorded. The
containerd install keeps 358 MiB less on disk, which is the compressed copy of both
versions that `docker load` leaves in containerd. The update streamed only the 13
layers v1 did not have.

**Check 6: crash right after install returns.** `echo b > /proc/sysrq-trigger` with
no sync, so the page cache is lost as in a power cut; the eMMC stays powered, so this
is milder than a real cut. 3 trials per arm, compared with a content hash of the
image taken after a clean install.

| Arm | Result |
|---|---|
| no `syncfs` | 3 of 3 broken: the record and all 17 snapshots are committed, but `docker create` fails. In trials 2 and 3 the error is `failed to stat parent: stat .../snapshots/1139/fs: no such file or directory`; trial 1 ran a probe that did not record the error text |
| `syncfs` before the records | 3 of 3 intact. The content hash matches the clean install |

`syncfs` (step 5) is required, not optional. Without it, a crash leaves an image that
looks installed and cannot run, and a reinstall would not repair it, because step 2
skips layers whose snapshot exists. Removing the image records let GC delete the
broken snapshots in all three trials.

The crash came after the records were created and the lease deleted. The content
hash covers the path, type, size and contents of every regular file, not
permissions, owners, link targets or xattrs. Cuts at other points are listed under
"Still unproven".

One change to the plan: importing the root package of the client adds 17 indirect
modules to `go.mod`, not three, and makes `golang.org/x/sys` direct; see "Code
changes" for the list.

### Results on the device, 2026-10-12 (minimum Docker version)

Same board, Docker v29.5.2, with `composectl` built from the implementation branch
(arm64), the node-red apps above copied to the board as a store.

The first run found a gate bug: `docker info` on this board reports the server
version as `v29.5.2`, the version comparison read that as `0.0.0`, and install
silently used `docker load` (the content store kept the compressed layers and the
records were the `docker load` ones). The comparison now strips a leading `v`.

The rerun with the fix took the containerd path:

| Step | Result |
|---|---|
| install v1 | 3m12s. Snapshots 0 → 17; content blobs 4 → 7 (index, manifest, config, no layers); records for the tag and the pinned digest, both targeting the OCI index; no lease left |
| `docker image ls --digests` | lists the image with tag, digest, ID and size |
| `check --install`, `ps` | no missing image |
| `run` (install + `docker compose up`) | the container starts from the installed image, no pull |
| install v2 (update) | 2m30s; only the 13 new layers streamed; both images listed |
| `uninstall --prune`, `rm` | board back to 0 images, 0 snapshots, 4 content blobs, no composectl leases |

The listing is therefore checked on 29.5.2 and 29.7.2; the minimum stays 29.5.2.

### Results on the device, 2026-10-13 (cuts during install, real composectl)

Same board and apps, `composectl` built from the reviewed tree. Every cut is a
sysrq-b with nothing synced, fired on the board itself: during unpack by a loop
that watches the snapshot directory count; between the first image record and the
lease delete by a probe that polls containerd's image service every 300 µs for the
pinned-digest record; right after `install` returns by the shell. The board came
back on its own each time (77–246 s) and the checks ran with no manual step.

| Cut | Trials | State after the reboot | Recovery |
|---|---|---|---|
| During unpack, at snapshot 3, 9 and 15 of 17 | 3 | Lease present, the 17 streamed layer blobs present. Snapshots torn in two trials: the metadata had 9 and 15 committed snapshots, the disk had 3 and 7 directories. `check --install` reports the image missing | The next `install` deleted the lease, GC dropped the torn snapshots, the image was unpacked again (17 snapshots, blobs back to 7), the container ran, `check` clean. 3 of 3 |
| After the first image record, before the lease delete | 3 | Both records, 17 snapshots, 24 blobs and the lease present; no bundle yet, so `check --install` reports the image present and `docker-compose.yml` missing | The next `install` (or `run`) deleted the stale lease, GC dropped the layers (blobs 7), the bundle was written, the container ran, `check` clean. 3 of 3 |
| Right after `install` returned | 5 | Records, snapshots and the lease deletion durable in all five; the image ran in all five. In two, the content metadata had 7 records but 24 files on disk: the unlinks of the 17 layer files were lost. The bundle was missing in all five | The next `install` rewrote the bundle. The orphan layer files survived that install's GC and were removed by the uninstall |

`docker image prune -a` with a stopped container of the app keeps the image (Docker
drops the tag record only); without a container it removes the image, `check
--install` reports it missing, and `install` brings it back from the store and the
container runs.

Two findings, neither introduced by this change:

1. The compose bundle is not durable when `install` returns: `InstallCompose`
   untars it without a sync, on both routes. A cut inside the writeback window
   loses it, `check --install` reports it, the next install rewrites it. Follow-up:
   `syncfs` on the compose root after the untar.
2. A cut between the GC's metadata commit and its unlinks leaves the compressed
   layers as files unknown to containerd (208 MB here). containerd removes such
   files only in `cleanupContent`, which walks the blob directory after a GC that
   removed a content record, so they stay until the next image removal on the
   device (the uninstall of the previous version at the next update, or `docker
   image prune`). Bounded to one install's compressed layers and self-healing.
   Follow-up option: let `composectl prune` force a content GC.

## Still unproven, ranked by risk

1. Real power cuts on the eMMC, and the fleet's snapshotter. The crash trials used a
   sysrq reboot and overlayfs only. If the fleet uses erofs (with its differ and
   fs-verity), the crash checks are repeated there before deployment; overlayfs
   results do not qualify erofs.
2. The storage precheck on separate mounts and on a nearly full filesystem (an app
   whose image config is unreadable is covered by the integration test).

1 is a deployment gate; 2 and the two follow-ups above are follow-ups.

## Integration test

Against the dockerd service on the containerd image store, stock and fio:

- install from the store with no network;
- an update that shares layers with the installed version;
- prune followed by an offline reinstall;
- a lease left by a killed install is removed on the next install, and the app runs;
- a second composectl waits for the install lock;
- a base snapshot removed by `docker image prune` between the scan and `Unpack`
  (the retry in step 4);
- an image pinned by an index;
- `ps`, `check --install`, `update cancel` and uninstall after a containerd install;
- the graphdriver path is unchanged (the existing tests).

## Accepted consequences (user, 2026-10-08)

- The persistent compressed copy in the store is acceptable for the first phases.
- `docker save` and `docker push` of app images on the device no longer work on the
  containerd image store: the layers are not in containerd.
- The fleet is new. Images loaded the old way, or pulled with `docker pull`, whose
  manifests carry layer labels, keep their compressed layers in containerd.
- composectl is the only writer of app images on devices. `docker pull` and
  `docker build` there are not supported for app images (see "Crash recovery").
- Concurrent composectl installs are serialized, not run in parallel.
- `/var/sota/reset-apps` and containerd's root are on the same mount on devices.
  composectl does not rely on this; the precheck merges terms by `st_dev`.
