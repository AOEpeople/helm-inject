
#!/bin/sh -e

# Copied w/ love from the excellent hypnoglow/helm-s3

if [ -n "${HELM_PUSH_PLUGIN_NO_INSTALL_HOOK}" ]; then
    echo "Development mode: not downloading versioned release."
    exit 0
fi

version="$(cat plugin.yaml | grep "version" | cut -d '"' -f 2)"
echo "Downloading and installing helm-inject ${version} ..."

url=""
if [ "$(uname)" = "Darwin" ]; then
    url="https://github.com/maorfr/helm-inject/releases/download/${version}/helm-inject-macos-${version}.tgz"
elif [ "$(uname)" = "Linux" ] ; then
    url="https://github.com/maorfr/helm-inject/releases/download/${version}/helm-inject-linux-${version}.tgz"
else
    url="https://github.com/maorfr/helm-inject/releases/download/${version}/helm-inject-windows-${version}.tgz"
fi

echo $url

mkdir -p "bin"
mkdir -p "releases/${version}"

# Download with curl if possible.
if [ -x "$(which curl 2>/dev/null)" ]; then
    curl -sSL "${url}" -o "releases/${version}.tgz"
else
    wget -q "${url}" -O "releases/${version}.tgz"
fi
tar xzf "releases/${version}.tgz" -C "releases/${version}"
mv "releases/${version}/bin/inj" "bin/inj" || \
    mv "releases/${version}/bin/inj.exe" "bin/inj"