# Installs the official Godot editor on a Windows runner and exports
# GODOT_PATH. Same behaviour and environment variables as install-godot.sh.
# Written for Windows PowerShell 5.1, which every Windows machine has.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # the progress bar makes downloads very slow
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Export-GodotPath([string]$Path) {
    # The runner documents UTF-8 for GITHUB_ENV; PowerShell 5.1 defaults to UTF-16.
    "GODOT_PATH=$Path" | Out-File -FilePath $env:GITHUB_ENV -Encoding utf8 -Append
}

if ($env:GODOT_PATH -and (Test-Path $env:GODOT_PATH)) {
    Write-Host "Using the Godot installed on this runner: $env:GODOT_PATH"
    Export-GodotPath $env:GODOT_PATH
    exit 0
}

$version = $env:GODOT_VERSION
if (-not $version) { throw 'GODOT_VERSION is not set' }
$base = $env:GODOT_DOWNLOAD_BASE
if (-not $base) { $base = "https://github.com/godotengine/godot/releases/download/$version-stable" }
$cache = Join-Path $env:RUNNER_TOOL_CACHE 'godot'
$dir = Join-Path $cache $version
$asset = "Godot_v$version-stable_win64.exe.zip"
$exe = Join-Path $dir "Godot_v$version-stable_win64.exe"

if (-not (Test-Path $exe)) {
    New-Item -ItemType Directory -Force -Path $cache | Out-Null
    # Download next to the cache so the final move is a rename.
    $tmp = Join-Path $cache ('download.' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading $asset from $base"
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
        Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA512-SUMS.txt" -OutFile (Join-Path $tmp 'SHA512-SUMS.txt')
        $want = $null
        foreach ($line in Get-Content (Join-Path $tmp 'SHA512-SUMS.txt')) {
            $fields = $line.Trim() -split '\s+'
            if ($fields.Count -ge 2 -and $fields[1] -eq $asset) { $want = $fields[0] }
        }
        if (-not $want) { throw "$asset is not listed in SHA512-SUMS.txt" }
        $got = (Get-FileHash -Algorithm SHA512 -Path (Join-Path $tmp $asset)).Hash
        if ($got -ne $want) { throw "SHA-512 of $asset does not match SHA512-SUMS.txt" } # -ne ignores case
        Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'out')
        # Another job on this machine may have installed it meanwhile.
        if (-not (Test-Path $exe)) {
            if (Test-Path $dir) { Remove-Item -Recurse -Force $dir }
            Move-Item (Join-Path $tmp 'out') $dir
        }
    }
    finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}

Export-GodotPath $exe
& $exe --headless --version
