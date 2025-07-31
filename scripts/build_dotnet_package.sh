#!/bin/bash

build_dotnet_package() {
    local package="$1"
    local version="$2"
    local git_url="$3"
    local project_path="$4"      # Path to .csproj relative to repo root (use "." for auto-detect)
    local extra_args="$5"         # Extra dotnet publish arguments
    local runtime="${6:-linux-x64}" # Target runtime identifier
    # All remaining arguments are artifact mappings in src:dest format
    
    local safe_version="${version//\//_}"
    local cache_dir="$BUILDDIR/${package}-${safe_version}-${runtime}"
    
    # Use cached artifacts if available
    if [ -n "$cache_dir" ] && [ -d "$cache_dir" ] && [ "$(ls -A "$cache_dir" 2>/dev/null)" ]; then
        echo "Using cached artifacts for $package version $version"
        for artifact_map in "${@:7}"; do
            local src="${artifact_map%%:*}"
            local dest="${artifact_map#*:}"
            local cached_name="$(echo "$src" | tr '/' '_')"
            
            mkdir -p "$(dirname "$DESTDIR$dest")"
            
            # Check if it's a cached directory (stored as tarball)
            if [ -f "$cache_dir/${cached_name}.tar.gz" ]; then
                # Extract directory from cache
                local clean_dest="${dest%/}"
                mkdir -p "$DESTDIR$clean_dest"
                tar -xzf "$cache_dir/${cached_name}.tar.gz" -C "$DESTDIR$clean_dest" --strip-components=1
                
                # Set executable permissions for binaries in bin directories
                if [[ "$clean_dest" == */bin/* ]] || [[ "$clean_dest" == */bin ]]; then
                    find "$DESTDIR$clean_dest" -type f -exec chmod +x {} \;
                fi
            elif [ -f "$cache_dir/$cached_name" ]; then
                # Copy cached file
                cp "$cache_dir/$cached_name" "$DESTDIR$dest"
                # Ensure executables have proper permissions
                if [[ "$dest" == */bin/* ]]; then
                    chmod +x "$DESTDIR$dest"
                fi
            else
                echo "Warning: Cached artifact not found: $cached_name"
            fi
        done
        return 0
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
        # Make it relative to build dir for mkosi-chroot
        project_file="${project_file#$build_dir/}"
    else
        project_file="$project_path"
    fi

    # Define build properties for reproducibility
    local build_props=(
        "-p:BuildTimestamp=0"
        "-p:Commit=0000000000000000000000000000000000000000"
        "-p:PublishSingleFile=true"
        "-p:PublishReadyToRun=true"
        "-p:DebugType=none"
        "-p:Deterministic=true"
        "-p:ContinuousIntegrationBuild=true"
        "-p:IncludeAllContentForSelfExtract=true"
        "-p:IncludePackageReferencesDuringMarkupCompilation=true"
        "-p:EmbedUntrackedSources=true"
        "-p:PublishRepositoryUrl=true"
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
            --output '/build/$package/publish' \
            ${build_props[*]} \
            $extra_args
    "

    # Copy artifacts to image and cache
    mkdir -p "$cache_dir"
    for artifact_map in "${@:7}"; do
        local src="${artifact_map%%:*}"
        local dest="${artifact_map#*:}"
        
        # Resolve source path (support wildcards and publish directory)
        local src_path=""
        if [[ "$src" == publish/* ]]; then
            # Look in publish directory
            src_path="$build_dir/$src"
        else
            # Look relative to build directory
            src_path="$build_dir/$src"
        fi
        
        # Handle wildcards
        local resolved_src=""
        if [[ "$src_path" == *"*"* ]]; then
            resolved_src=$(find "$(dirname "$src_path")" -name "$(basename "$src_path")" | head -n1)
        else
            resolved_src="$src_path"
        fi
        
        if [ ! -e "$resolved_src" ]; then
            echo "Error: Source artifact not found: $src"
            return 1
        fi

        # Copy the built artifact to the destination
        mkdir -p "$(dirname "$DESTDIR$dest")"
        
        # Handle both files and directories
        if [ -d "$resolved_src" ]; then
            # For directories, ensure destination doesn't have trailing slash issues
            local clean_dest="${dest%/}"
            cp -r "$resolved_src" "$DESTDIR$clean_dest"
            
            # Set executable permissions for binaries in bin directories
            if [[ "$clean_dest" == */bin/* ]] || [[ "$clean_dest" == */bin ]]; then
                find "$DESTDIR$clean_dest" -type f -exec chmod +x {} \;
            fi
            
            # Cache directory as tarball
            tar -czf "$cache_dir/$(echo "$src" | tr '/' '_').tar.gz" -C "$(dirname "$resolved_src")" "$(basename "$resolved_src")"
        else
            # For files
            cp "$resolved_src" "$DESTDIR$dest"
            
            # Ensure executables have proper permissions
            if [[ "$dest" == */bin/* ]]; then
                chmod +x "$DESTDIR$dest"
            fi
            
            # Cache file
            cp "$resolved_src" "$cache_dir/$(echo "$src" | tr '/' '_')"
        fi
    done
    
    # Clean up temporary publish directory
    rm -rf "$build_dir/publish"
}

# Helper function to maintain backward compatibility
build_dotnet_binary() {
    local package="$1"
    local version="$2"
    local git_url="$3"
    local provided_binary="$4"
    local project_path="${5:-.}"
    local extra_args="${6:-}"
    local runtime="${7:-linux-x64}"
    
    # If a binary is provided, copy it directly
    if [ -n "$provided_binary" ]; then
        echo "Using provided binary for $package"
        mkdir -p "$DESTDIR/usr/bin"
        cp "$provided_binary" "$DESTDIR/usr/bin/$package"
        chmod +x "$DESTDIR/usr/bin/$package"
        return
    fi
    
    # Otherwise use the new function with a single artifact mapping
    build_dotnet_package "$package" "$version" "$git_url" "$project_path" "$extra_args" "$runtime" \
        "publish/$package:/usr/bin/$package"
}