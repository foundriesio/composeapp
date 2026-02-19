# Utility To Manage Compose Apps

This package provides both a library and a CLI utility to manage Compose Apps built by [FoundriesFactory](https://foundries.io/).
For more detailed information about the FoundriesFactory® Compose App, please refer to [the user documentation](https://docs.foundries.io/latest/tutorials/compose-app/compose-app.html).
Essentially, a FoundriesFactory Compose App adheres to [the Compose specification](https://github.com/compose-spec/compose-spec/blob/master/spec.md).
FoundriesFactory offers mechanisms for building, packaging, and distributing Compose Apps in the form of [OCI images](https://github.com/opencontainers/image-spec/blob/main/spec.md).
This utility enables users to pull Compose Apps from the [FoundriesFactory® App Hub](https://hub.foundries.io/) and manage them on a device or a local host.
This includes tasks such as installation, running, stopping, etc.

## Installation

### Install From APT (Debian/Ubuntu)

1. Update the `apt` package index and install packages needed to use the fioup `apt` repository:

   ```
   sudo apt update
   sudo apt install -y apt-transport-https ca-certificates curl gnupg
   ```

1. Download the public signing key for the package repositories:

   ```
   curl -L https://fioup.foundries.io/pkg/deb/dists/stable/Release.gpg | sudo gpg --dearmor -o /etc/apt/trusted.gpg.d/fioup-stable.gpg
   ```

1. Add the appropriate `apt` repository.

   ```
   echo 'deb [signed-by=/etc/apt/trusted.gpg.d/fioup-stable.gpg] https://fioup.foundries.io/pkg/deb stable main' | sudo tee /etc/apt/sources.list.d/fioup.list
   ```

1. Install composectl

   ```
   sudo apt update && sudo apt install composectl
   ```

### Install The Development Version (from source)

```commandline
git clone https://github.com/foundriesio/composeapp.git
```

```commandline
cd composeapp && make
```

As a result, the `composectl` binary should appear in the `./bin` directory.

## Usage

### Structure

Compose Apps' data are spread across three locations on a local file system:

1. The App store directory — all app blobs are stored in this location, by default it is `~/.composeapps/store`.
2. The App project or compose directory — where the Docker compose YAML along with its complementary files are stored, by default in `~/.composeapps/projects`.
3. The Docker engine store — a few sub-directories in the Docker engine data root, by default in `/var/lib/docker`.

### Configuration

The App store and project directories can be specified via the `--store/-s` and `--compose/-i` parameters respectively.
The Docker engine store is determined by the Docker daemon instance that the utility communicates to via a socket.

By default, the utility talks to the Docker daemon through `unix:///var/run/docker.sock`,
which in turn stores image layers and containers data under `/var/lib/docker`.
The Docker daemon socket can be specified in the `--host/-H` parameter.
Also, the `composectl` utility respects `DOCKER_HOST` environment variable, which is another way to specify the Docker daemon socket.

### Authentication

Prior to communicating with the [FoundriesFactory App Hub](https://hub.foundries.io/) authentication should be set.
It can be done either by logging at the hub `docker login hub.foundries.io -u "doesnotmatter" -p <FoundriesFactory token>`
or setting the Docker credential helper by running `fioctl configure-docker` command.

The `FoundriesFactory token` can be obtained at <https://app.foundries.io/settings/tokens/>.

The both methods updates a Docker configuration file on a local host (e.g. `~/.docker/config.json`).
Make sure to backup it before running the aforementioned commands.

### Pulling App

Once the authentication is set, Compose App can be pulled for the hub. At first, the App's URI should be found.
To do so, a user can run `fioctl targets list` and `fioctl targets show compose-app <version> <app name>`.
The last command outputs the target's app URI.

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
