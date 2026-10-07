# Utility To Manage Compose Apps

composeapp provides a Go library and the `composectl` CLI to package, distribute,
and manage Docker Compose applications through compatible OCI container registries.
It can be used for local development, CI/CD delivery, and deploying applications
on servers or devices using a public registry or a private registry you operate.

A Compose App packages a project defined using the
[Compose specification](https://github.com/compose-spec/compose-spec/blob/master/spec.md)
as an [OCI artifact](https://github.com/opencontainers/image-spec/blob/main/spec.md).
It contains a bundle of the Compose project and supporting files, with references to
the service images pinned by digest. `composectl` pulls and checks the app's
blobs, installs its files and images, and manages its services through Docker Compose.

For FoundriesFactory authentication and target discovery, see
[Using composectl with FoundriesFactory](docs/foundriesfactory.md).

## Installation

Running apps requires Docker Engine and Docker Compose v2, with the `docker`
command available on your host.

### Install From APT (Debian/Ubuntu)

Foundries maintains an APT repository containing the `composectl` package.
These packages can be used with any compatible registry.

1. Update the package index and install the prerequisites:

   ```sh
   sudo apt update
   sudo apt install -y apt-transport-https ca-certificates curl gnupg
   ```

2. Download the public signing key for the repository:

   ```sh
   curl -L https://fioup.foundries.io/pkg/deb/dists/stable/Release.gpg | sudo gpg --dearmor -o /etc/apt/trusted.gpg.d/fioup-stable.gpg
   ```

3. Add the repository:

   ```sh
   echo 'deb [signed-by=/etc/apt/trusted.gpg.d/fioup-stable.gpg] https://fioup.foundries.io/pkg/deb stable main' | sudo tee /etc/apt/sources.list.d/fioup.list
   ```

4. Install `composectl`:

   ```sh
   sudo apt update && sudo apt install composectl
   ```

### Install A Release

Linux binaries and Debian packages for amd64 and arm64 are available from the
[project releases](https://github.com/foundriesio/composeapp/releases).

### Install The Development Version (from source)

```commandline
git clone https://github.com/foundriesio/composeapp.git
```

```commandline
cd composeapp
./dev-shell.sh make
```

As a result, the `composectl` binary should appear in the `./bin` directory.

## Usage

### Structure

Compose Apps' data are spread across three locations on a local file system:

1. The App store directory — stores downloaded app blobs, including the Compose
   bundle and service image content (manifests, configs, image layer blobs).
   Defaults to `~/.composeapps/store`.
2. The App project directory (Compose runtime directory) — contains each installed
   app's extracted Compose YAML and supporting files. Docker Compose runs from
   the app's subdirectory, by default `~/.composeapps/projects/<app name>`.
3. The Docker engine store — a few sub-directories in the Docker engine data root, by default in `/var/lib/docker`.

### Configuration

The App store and project directories can be specified via the `--store/-s` and `--compose/-i` parameters respectively.
The Docker engine store is determined by the Docker daemon instance that the utility communicates to via a socket.

By default, the utility talks to the Docker daemon through `unix:///var/run/docker.sock`,
which in turn stores image layers and containers data under `/var/lib/docker`.
Set `DOCKER_HOST` to select a different daemon for both `composectl` and the
Docker Compose commands it launches. The `--host/-H` parameter selects the daemon
for operations using the Docker API.

### Authentication

For a registry that requires authentication, log in using its hostname and the
credentials or token provided by its operator:

```sh
docker login registry.example.com
```

`composectl` uses Docker's credential configuration, including configured
credential helpers. Back up your Docker configuration (usually
`~/.docker/config.json`) before changing it. Configure credentials for the app
registry and any other registries hosting its service images. Public repositories
may allow pulling without authentication.

### Publishing An App

If you already have a published app URI, proceed to [Pulling App](#pulling-app).
Otherwise, run the following from your project directory, containing
`docker-compose.yml` and its supporting files:

```sh
composectl publish registry.example.com/apps/myapp:1.0
```

Replace `registry.example.com/apps/myapp:1.0` with a repository and tag you can
write to. Each service must reference an image already published in a registry,
using a tag or digest. Publishing resolves service image references to digests
and uploads the Compose bundle; it does not build or upload your service images.
Use `.composeappignores` to exclude files from the bundle.

The command prints the app URI as
`registry.example.com/apps/myapp@sha256:<digest>`. Save this URI to identify the
exact app version when pulling it on another host.

### Pulling App

Use the app URI from `composectl publish` or from the app's publisher. It must
include the manifest digest, for example
`registry.example.com/apps/myapp@sha256:<digest>`, rather than just a tag.

```commandline
composectl pull <app URI> [<app URI>]
```

The pull command fetches all apps specified in the parameter list. The apps' blobs are stored in the App store.
The utility checks integrity of the pulled App, it implies checking integrity of each App's blobs/elements during the pull process.

### Managing App

Once the app is present in the local app store, a user can perform the actions detailed below over it.

#### Check App Integrity

Effectively, the command checks integrity (sha-256 hash) of each app blob, i.e. an overall app's Merkle tree, starting from the top level element—the app manifest.

```commandline
composectl check <app URI> [<app URI>]
```

#### Install and Uninstall App

```commandline
composectl install <app URI>
```

```commandline
composectl uninstall <app URI | app name>
```

#### Run and Stop App

```commandline
composectl run <app name | app URI> [<app name | app URI>] | --apps=<comma,separated,app,list>; --apps="" - run all apps
```

```commandline
composectl stop <app name> [<app name>] | --all
```

#### Remove App and Prune Store

```commandline
composectl rm <app name | app URI> [<app name | app URI>] [--prune]
```

```commandline
composectl prune
```

## Development and Testing

The dev & test environment based on Docker compose contains all required elements to build, manually test, as well as run automated tests.
To launch the environment and enter into its shell just run:

```commandline
./dev-shell.sh
```

It will take some time to start the environment for the first time because:

1. The Docker daemon (dind) and the registry container (distribution) images have to be pulled.
2. The dev&test container should be built.

Once you are logged into the container shell you can build and run the `composectl` utility, test it manually
and run automated tests:

```commandline
./dev-shell.sh
make test-e2e
```
