# Using composectl with FoundriesFactory

[FoundriesFactory](https://foundries.io/) builds and distributes Compose Apps
through its App Hub. `composectl` can pull these apps and manage them on a device
or local host using the same workflow described in the [main documentation](../README.md).

For details about creating Compose Apps in FoundriesFactory, see the
[FoundriesFactory Compose App documentation](https://docs.foundries.io/latest/tutorials/compose-app/compose-app.html).

## Install From APT (Debian/Ubuntu)

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

## Authenticate To The App Hub

Before pulling private apps from [the App Hub](https://hub.foundries.io/),
configure authentication using either a FoundriesFactory token or the Docker
credential helper provided by `fioctl`.

You can obtain a token from the
[FoundriesFactory token settings](https://app.foundries.io/settings/tokens/).
Log in with the following command and enter the token at the password prompt:

```sh
docker login hub.foundries.io -u doesnotmatter
```

Alternatively, configure the credential helper:

```sh
fioctl configure-docker
```

Both methods update your Docker configuration, usually `~/.docker/config.json`.
Back it up before changing it. `composectl` uses this configuration to authenticate
to the App Hub.

## Find And Pull An App

List your factory's targets and inspect an app in a selected target:

```sh
fioctl targets list
fioctl targets show compose-app <version> <app name>
```

The second command outputs the app URI, including its manifest digest. Use it
to pull the app:

```sh
composectl pull <app URI>
```

Then follow the [app management instructions](../README.md#managing-app) to check,
install, run, stop, uninstall, or remove it.
