#!/bin/sh -e
## Used to create a debian package archive layout to publish new debs. It
## assumes the "release-prep-archive.sh" script has been run before hand to
## prepare the content
## Required keys in <keys dir> are:
## 1. <keys dir>/bot.foundries.pub.gpg
## 2. <keys dir>/bot.foundries.signing.secret.gpg
## 3. "gpgpass" environment variable with the signing key password should be set

if [ $# -ne 2 ] ; then
	echo "Usage $0 <keys dir> <package-repo-dir>"
	exit 0
fi

if [ -z "$gpgpass" ] ; then
	echo "gpgpass environment variable not defined"
	exit 0
fi

keysdir=$1
pubdir=$2

do_hash() {
    HASH_NAME=$1
    HASH_CMD=$2
    echo "${HASH_NAME}:"
    for f in $(find -type f); do
        f=$(echo $f | cut -c3-) # remove ./ prefix
        if [ "$f" = "Release" ]; then
            continue
        fi
        echo " $(${HASH_CMD} ${f}  | cut -d" " -f1) $(wc -c $f)"
    done
}

docker run --rm -i \
	-v ${keysdir}:/keys:ro \
	-v ${pubdir}/pkg/deb:/layout \
	debian:trixie  <<EOF
set -ex

apt update && apt install -y dpkg-dev

mkdir -p /layout/dists/stable/main/binary-amd64
mkdir -p /layout/dists/stable/main/binary-arm64

cd /layout
dpkg-scanpackages --multiversion --arch amd64 pool/ > dists/stable/main/binary-amd64/Packages
dpkg-scanpackages --multiversion --arch arm64 pool/ > dists/stable/main/binary-arm64/Packages

chown -R $(id -u):$(id -g) /layout
EOF

cd ${pubdir}/pkg/deb/dists/stable
cat >Release <<EOF
Origin: Fioup Debian Repository
Suite: stable
Architectures: amd64 arm64
Components: main
Date: $(date -Ru)
EOF
do_hash "MD5Sum" "md5sum" >> Release
do_hash "SHA1" "sha1sum" >> Release
do_hash "SHA256" "sha256sum" >> Release

docker run --rm -i \
	-v ${keysdir}:/keys:ro \
	-v ${pubdir}/pkg/deb:/layout \
	debian:trixie <<EOF
set -ex

apt update && apt install -y gnupg2
gpg2 --import /keys/bot.foundries.pub.gpg
echo ${gpgpass} | gpg2 --batch --passphrase-fd 0 --import /keys/bot.foundries.signing.secret.gpg

cd /layout/dists/stable
gpg --armor --export > Release.gpg
echo ${gpgpass} | gpg --batch --pinentry-mode loopback --passphrase-fd 0 --clearsign -o - Release > InRelease
EOF

echo Everything is staged. Now run: gsutil -m rsync -r ${pubdir}/ gs://fioup.foundries.io/
