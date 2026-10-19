# Install into containerd: link the layers in

The containerd install (`containerd-install-plan-2026-10-08.md`) no longer copies the
compressed layers from composectl's store into containerd's content store. Each layer
is hardlinked instead, so an install writes nothing but the snapshots.

## How

1. Check that the store blob exists and has the layer's size; otherwise the install
   fails.
2. Hardlink it to `<content root>/blobs/<alg>/<hex>`. A file already there is
   containerd's own blob or an earlier link and is used as it is.
3. Register it under the install lease. With containerd's default `shared` content
   policy a writer for a blob already at its path copies nothing and does not hash it.
   The blob was verified when it was pulled, and the unpack checks every extracted
   layer against the image's diff IDs. If containerd's blob cleanup removed the file
   in between, the layer is linked and registered once more; a second miss fails the
   install.

Deleting the lease lets containerd's GC unlink its name only; the store keeps its own.

## Limit

The store and the content store must be on one filesystem: a link across filesystems
fails the install, naming both paths. The test environment keeps its store on the test
dockerd's data volume for that.

## Per-layer progress

Beside the unpack, a watcher polls the image's snapshots every 100 ms. The first
layer without a snapshot is reported as extracting, with the space its snapshot has
taken from the snapshotter's filesystem against the layer's tar size (from the gzip
trailer for a gzip layer), or with the elapsed time when that size is unknown. The
differ's reads cannot be observed from outside containerd, so the bar is an
estimate. A layer whose snapshot appears is reported loaded with the time it took.
