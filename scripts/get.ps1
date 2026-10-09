# Installs a pitwall release on Windows; see "Install" in README.md.

function Get-PitwallAsset([string]$Arch) {
    switch ($Arch) {
        'AMD64' { 'pitwall_windows_amd64.zip' }
        'ARM64' { 'pitwall_windows_arm64.zip' }
        default { throw "pitwall: no release for $Arch" }
    }
}

# Writes a shortcut at $Path that opens a pitwall window from $Exe, with the
# icon built into it.
function New-PitwallShortcut([string]$Exe, [string]$Path) {
    $s = (New-Object -ComObject WScript.Shell).CreateShortcut($Path)
    $s.TargetPath = $Exe
    $s.IconLocation = "$Exe,0"
    $s.WorkingDirectory = $env:USERPROFILE
    $s.Description = 'Terminal multiplexer for coding agents'
    $s.Save()
}

function Install-Pitwall {
    # why: iex runs this in the caller's session, so preferences stay local here.
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repo = 'quanticstudios/pitwall'
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    $name = Get-PitwallAsset $arch
    $version = $env:PITWALL_VERSION
    if ($version) {
        $base = "https://github.com/$repo/releases/download/$version"
    } else {
        $base = "https://github.com/$repo/releases/latest/download"
        $version = 'latest'
    }
    $dir = $env:PITWALL_INSTALL_DIR
    if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA 'pitwall\bin' }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('pitwall-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading $name ($version)"
        $zip = Join-Path $tmp $name
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$name" -OutFile $zip
        Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')
        $want = $null
        foreach ($line in Get-Content (Join-Path $tmp 'checksums.txt')) {
            $f = -split $line
            if ($f.Count -ge 2 -and ($f[1] -eq $name -or $f[1] -eq "*$name")) { $want = $f[0] }
        }
        $got = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash
        if (-not $want -or $want -ne $got) { throw "pitwall: $name does not match checksums.txt; nothing installed" }
        Expand-Archive -Path $zip -DestinationPath (Join-Path $tmp 'x')
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        $exe = Join-Path $dir 'pitwall.exe'
        # why: Windows refuses to overwrite a running exe but lets it be renamed.
        # An older one a daemon still runs keeps its name; pitwall deletes them all later.
        if (Test-Path $exe) {
            $old = "$exe.old"
            for ($i = 1; (Test-Path $old) -and $i -lt 10; $i++) {
                Remove-Item -Force $old -ErrorAction SilentlyContinue
                if (Test-Path $old) { $old = "$exe.old$i" }
            }
            Move-Item -Force $exe $old
        }
        Copy-Item (Join-Path $tmp 'x\pitwall.exe') $exe
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $dir) {
        $newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        $env:Path = "$env:Path;$dir"
        Write-Host "Added $dir to your user PATH; new terminals pick it up."
    }
    New-PitwallShortcut $exe (Join-Path ([Environment]::GetFolderPath('Programs')) 'pitwall.lnk')
    Write-Host "Installed $(& $exe --version) in $dir, with pitwall in the Start menu."
    Write-Host 'Next: pitwall hooks install'
    Write-Host 'Then run pitwall and trust Codex hooks once with /hooks.'
}

# why: the install runs last, so a download cut short runs nothing.
if ($env:PITWALL_GET_TEST -ne '1') { Install-Pitwall }
