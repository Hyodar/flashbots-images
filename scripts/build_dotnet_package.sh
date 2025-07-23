#!/bin/bash

build_dotnet_package() {
    local package="$1"
    local version="$2"
    local git_url="$3"
    local provided_binary="$4"
    local project_path="${5:-.}"  # Path to .csproj relative to repo root
    local extra_args="${6:-}"      # Extra dotnet publish arguments
    local runtime="${7:-linux-x64}" # Target runtime identifier

    local dest_path="$DESTDIR/usr/bin/$package"
    mkdir -p "$DESTDIR/usr/bin"

    # If binary path is provided, use it directly
    if [ -n "$provided_binary" ]; then
        echo "Using provided binary for $package"
        cp "$provided_binary" "$dest_path"
        chmod +x "$dest_path"
        return
    fi

    # If binary is cached, skip compilation
    local cached_binary="$BUILDDIR/${package}-${version}-${runtime}"
    if [ -f "$cached_binary" ]; then
        echo "Using cached binary for $package version $version"
        cp "$cached_binary" "$dest_path"
        return
    fi

    # Clone the repository
    local build_dir="$BUILDROOT/build/$package"
    mkdir -p "$build_dir"
    git clone --depth 1 --branch "$version" "$git_url" "$build_dir"

    # Find the project file if not specified
    local project_file=""
    if [ "$project_path" = "." ]; then
        # Auto-detect project file
        project_file=$(find "$build_dir" -name "*.csproj" -o -name "*.fsproj" | head -n1)
        if [ -z "$project_file" ]; then
            echo "Error: No .csproj or .fsproj file found in $build_dir"
            return 1
        fi
    else
        project_file="$build_dir/$project_path"
    fi

    # Define build properties for reproducibility
    local build_props=(
        "-p:BuildTimestamp=0"
        "-p:Commit=0000000000000000000000000000000000000000"
        "-p:PublishSingleFile=true"
        # "-p:PublishTrimmed=true"
        "-p:PublishReadyToRun=true"
        "-p:DebugType=none"
        "-p:DebugSymbols=false"
        "-p:EnableCompressionInSingleFile=true"
        "-p:IncludeNativeLibrariesForSelfExtract=true"
        "-p:StripSymbols=true"
        "-p:TrimMode=full"
        "-p:InvariantGlobalization=true"
        "-p:Deterministic=true"
        "-p:ContinuousIntegrationBuild=true"
        "-p:IncludePackageReferencesDuringMarkupCompilation=true"
        "-p:EmbedUntrackedSources=true"
    )

    # Build inside mkosi chroot
    mkosi-chroot bash -c "
        export DOTNET_CLI_TELEMETRY_OPTOUT=1 \
               DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
               DOTNET_NOLOGO=1 \
               DOTNET_CLI_HOME='/tmp/dotnet' \
               NUGET_PACKAGES='/tmp/nuget'
        
        cd '/build/$package'
        
        # Restore dependencies
        dotnet restore '$project_file' \
            --runtime '$runtime' \
            --disable-parallel \
            --force
        
        # Publish the application
        dotnet publish '$project_file' \
            --configuration Release \
            --runtime '$runtime' \
            --self-contained true \
            --output '/tmp/publish' \
            ${build_props[*]} \
            $extra_args
    "

    # Find the published binary
    local published_binary=""
    if [ -f "$build_dir/../../../tmp/publish/$package" ]; then
        published_binary="$build_dir/../../../tmp/publish/$package"
    else
        # Try to find the binary with common patterns
        published_binary=$(find "$build_dir/../../../tmp/publish" -type f -executable -name "$package*" | head -n1)
    fi

    if [ -z "$published_binary" ] || [ ! -f "$published_binary" ]; then
        echo "Error: Could not find published binary for $package"
        return 1
    fi

    # Cache and install the built binary
    install -m 755 "$published_binary" "$cached_binary"
    install -m 755 "$cached_binary" "$dest_path"
    
    # Clean up temporary publish directory
    rm -rf "$build_dir/../../../tmp/publish"
}

# Example usage:
# build_dotnet_package "myapp" "v1.0.0" "https://github.com/user/myapp.git" "" "src/MyApp/MyApp.csproj"
