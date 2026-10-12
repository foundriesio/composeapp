# Installing app images into containerd

On a dockerd with the containerd image store, `docker load` keeps every app image
compressed twice: in composectl's store and in containerd's content store.
`composectl install` instead writes the images from the store into the containerd
store under a lease, unpacks them and deletes the lease, so containerd's GC drops
the compressed layers. The store copy is the only compressed one left, and an image
pruned from dockerd is reinstalled from it without network.

## When it is used

The containerd route is taken when dockerd uses the containerd image store, reports
a containerd address, is reached on a local socket (`unix://` or `npipe://`) and is
Docker 29.5.2 or newer, the oldest version whose `docker image ls` lists images
whose layers are not in containerd. Otherwise install uses `docker load`. If
containerd cannot be reached on such a daemon, install fails instead of falling back.

## Install

Under an exclusive flock on `<StoreRoot>/.install.lock`, which uninstall, `rm` and
`prune` take too, for each app:

1. Delete every leftover `composectl/install/` lease, then create the lease
   `composectl/install/<app>@<digest>` with no expiry (a board without a real-time
   clock can jump forward in time).
2. For each image, write under the lease the layers from the first one without a
   snapshot on, the config, the manifest and the index. The manifest has no layer
   GC labels, so only the lease holds the layers.
3. Unpack the image. If a base snapshot was removed in the meantime, write every
   layer and unpack once more.
4. `syncfs` the snapshotter root.
5. Create or update the image records: the digest ref and the tag ref.
6. Delete the lease; containerd's GC drops the compressed layers.

## Crash recovery

The image records are created only after the `syncfs`, so a recorded image is on
disk. Without it, a crash right after install left 3 of 3 images broken on the board.
The snapshots of an interrupted install are held only by its lease, which the next
install, or `composectl prune`, deletes before anything is reused.

## Storage check

`check` sizes what an install writes on each filesystem: the missing blobs in the
store, the app bundle in the compose root, the compressed layers to write in
containerd's content root (an over-estimate where they are hardlinked from the
store) and the new unpacked snapshots in the snapshotter root. Terms on one
filesystem are summed and compared with its free space, and `check --format json`
reports them in a `storage` array (`path`, `required`, `free`, `ok`). With `docker
load` the single check of the store root stays.

## Limits

- `docker save` and `docker push` of app images do not work: the compressed layers
  are not in containerd.
- Images loaded with `docker load` or pulled with `docker pull` keep their
  compressed layers.
- A snapshot that `docker pull` or `docker build` created and a crash broke is not
  detected.
- The compose bundle is not synced when install returns.
- Power cuts were tested on overlayfs only.
