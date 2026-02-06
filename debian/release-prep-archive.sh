#!/bin/sh -e
## Used to download and prepare a composectl release for publishing. It:
##  * rsyncs current package archive contents locally
##  * copyies latest .debs from github into local archive
## Things will be formatted for the release-publish-archive.sh script to process

if [ $# -ne 2 ] ; then
	echo "Usage: $0 <package-repo-dir> <version>"
	echo "  example: $0 bin/package-repo-dir 1.1.0"
	exit 0
fi

releasedir=$1
version=$2
version=${version#v} # make sure its 0.1.0 not v0.1.0

gsutil -m rsync -r gs://fioup.foundries.io/ ${releasedir}/

url="https://github.com/foundriesio/composeapp/releases/download/v${version}/composectl_${version}_amd64.deb"
wget -O ${releasedir}/pkg/deb/pool/composectl_${version}_amd64.deb $url

url="https://github.com/foundriesio/composeapp/releases/download/v${version}/composectl_${version}_arm64.deb"
wget -O ${releasedir}/pkg/deb/pool/composectl_${version}_arm64.deb $url
