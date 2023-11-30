# Package and a CLI utility `composectl` to manage compose Apps

TODO

## Build

```
make
```

## Usage
```
./bin/composectl check <uri> [<uri>]
./bin/composectl pull <uri> [<uri>]
./bin/composectl ls
./bin/composectl install <uri>
```

By default,
* app blobs are stored in ~/.composeapps/store;
* app compose project is installed to ~/.composeapps/projects and its images are laoded to the docker daemon listening on `$DOCKER_HOST` or `unix:///var/run/docker.sock`.

The `install` requires the pacthed version of the docker daemon, otherwise the image loading fails (TBD).
