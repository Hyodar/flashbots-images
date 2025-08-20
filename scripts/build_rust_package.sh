#!/bin/bash

build_rust_package() {
    local identifier="$1"
    local version="$2"
    local git_url="$3"
    local provided_binary="$4"
    local extra_features="${5:-}"
    local extra_rustflags="${6:-}"
    local workspace_package="${7:-}"

    # Parse identifier - can be "binary:package" or just "package"
    local binary_name="${identifier%%:*}"
    local package_name="${identifier#*:}"    
    if [ "$binary_name" = "$package_name" ]; then
        package_name="$identifier"
    fi

    local safe_version="${version//\//_}"

    local dest_path="$DESTDIR/usr/bin/$package_name"
    mkdir -p "$DESTDIR/usr/bin"

    # If binary path is provided, use it directly
    if [ -n "$provided_binary" ]; then
        echo "Using provided binary for $package_name"
        cp "$provided_binary" "$dest_path"
        return
    fi

    # If binary is cached, skip compilation
    local cached_binary="$BUILDDIR/${binary_name}-${safe_version}"
    if [ -f "$cached_binary" ]; then
        echo "Using cached binary for $binary_name version $version"
        cp "$cached_binary" "$dest_path"
        return
    fi

    # Clone the repository
    local build_dir="$BUILDROOT/build/$package_name"
    mkdir -p "$build_dir"
    git clone --depth 1 --branch "$version" "$git_url" "$build_dir"

    # Define Rust flags for reproducibility
    local rustflags=(
        "-C target-cpu=generic"
        "-C link-arg=-Wl,--build-id=none"
        "-C symbol-mangling-version=v0"
        "-L /usr/lib/x86_64-linux-gnu"
    )

    # Build inside mkosi chroot
    mkosi-chroot bash -c "
        export RUSTFLAGS='${rustflags[*]} ${extra_rustflags}' \
               CARGO_PROFILE_RELEASE_LTO='thin' \
               CARGO_PROFILE_RELEASE_CODEGEN_UNITS='1' \
               CARGO_PROFILE_RELEASE_PANIC='abort' \
               CARGO_PROFILE_RELEASE_INCREMENTAL='false' \
               CARGO_PROFILE_RELEASE_OPT_LEVEL='3' \
               CARGO_TERM_COLOR='never' \
               CARGO_HOME='/build/.cargo'
        cd '/build/$package_name'
        cargo fetch
        cargo build --release --frozen ${extra_features:+--features $extra_features} ${workspace_package:+--package $workspace_package}
    "

    # Cache and install the built binary
    install -m 755 "$build_dir/target/release/$binary_name" "$cached_binary"
    install -m 755 "$cached_binary" "$dest_path"
}