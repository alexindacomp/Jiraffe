# Jiraffe installer for the current user (no admin rights needed).
#
#   irm https://github.com/alexindacomp/Jiraffe/releases/latest/download/install.ps1 | iex
#
# Downloads the latest jiraffe.exe from GitHub releases, checks its SHA-256,
# installs it to %LOCALAPPDATA%\Programs\Jiraffe, adds Start menu and desktop
# shortcuts and starts it. Running it again updates an existing installation.
#
# "Install Jiraffe.cmd" in the release archive runs this script with
# JIRAFFE_SOURCE set to the jiraffe.exe next to it: then nothing is downloaded.
# The file is downloaded by PowerShell itself, so it carries no "downloaded from
# the internet" mark and SmartScreen does not stop the first start. Jiraffe then
# updates itself (signed releases only).
#
# Keep this file ASCII-only: `irm | iex` may decode it with a legacy code page.
# Never use `exit` here: under `iex` it would close the user's PowerShell window.

function Install-Jiraffe {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'  # Invoke-WebRequest is very slow with the progress bar on PS 5.1

    $base = 'https://github.com/alexindacomp/Jiraffe/releases/latest/download'
    if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set.' }
    $dir = Join-Path $env:LOCALAPPDATA 'Programs\Jiraffe'
    $exe = Join-Path $dir 'jiraffe.exe'

    # Windows PowerShell 5.1 may default to TLS 1.0; GitHub needs TLS 1.2.
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch { }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('jiraffe-install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null
    try {
        $newExe = Join-Path $tmp 'jiraffe.exe'
        $source = $env:JIRAFFE_SOURCE
        if ($source) {
            # Installing from the release archive: use the exe next to the installer.
            if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "jiraffe.exe was not found next to the installer ($source). Extract the whole archive first." }
            Copy-Item -LiteralPath $source -Destination $newExe -Force
        } else {
            Write-Host 'Downloading Jiraffe...'
            $sumFile = Join-Path $tmp 'jiraffe.exe.sha256'
            Invoke-WebRequest -UseBasicParsing -Uri "$base/jiraffe.exe" -OutFile $newExe
            Invoke-WebRequest -UseBasicParsing -Uri "$base/jiraffe.exe.sha256" -OutFile $sumFile

            # "<64 hex>  jiraffe.exe" (sha256sum format)
            $line = (Get-Content -LiteralPath $sumFile -TotalCount 1).Trim()
            if ($line -notmatch '^([0-9a-fA-F]{64})\s') { throw 'The checksum file has an unexpected format.' }
            $want = $Matches[1].ToLowerInvariant()
            $got = (Get-FileHash -LiteralPath $newExe -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($got -ne $want) { throw "Checksum mismatch: expected $want, got $got. Nothing was installed." }
        }
        $head = [IO.File]::ReadAllBytes($newExe)[0..1]
        if ($head[0] -ne 0x4D -or $head[1] -ne 0x5A) { throw 'The download is not a Windows program. Nothing was installed.' }

        # Close a running Jiraffe (any location): it is a single-instance app.
        $running = @(Get-Process -Name 'jiraffe' -ErrorAction SilentlyContinue)
        if ($running.Count -gt 0) {
            Write-Host 'Closing the running Jiraffe...'
            $running | Stop-Process -Force -ErrorAction SilentlyContinue
            $running | Wait-Process -Timeout 15 -ErrorAction SilentlyContinue
        }

        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        if (Test-Path -LiteralPath $exe) {
            # A running or locked exe can still be renamed; Jiraffe removes *.old itself later.
            $old = "$exe.old"
            Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $old) { $old = "$exe.old." + [DateTime]::Now.ToString('yyyyMMddHHmmss') }
            Move-Item -LiteralPath $exe -Destination $old -Force
        }
        Move-Item -LiteralPath $newExe -Destination $exe -Force
        Unblock-File -LiteralPath $exe -ErrorAction SilentlyContinue

        $shell = New-Object -ComObject WScript.Shell
        foreach ($folder in @([Environment]::GetFolderPath('Programs'), [Environment]::GetFolderPath('Desktop'))) {
            if (-not $folder) { continue }
            $lnk = $shell.CreateShortcut((Join-Path $folder 'Jiraffe.lnk'))
            $lnk.TargetPath = $exe
            $lnk.WorkingDirectory = $dir
            $lnk.Description = 'Jiraffe - Jira companion'
            $lnk.Save()
        }

        Start-Process -FilePath $exe -WorkingDirectory $dir
        Write-Host "Jiraffe is installed to $dir and started (tray icon). Shortcuts are in the Start menu and on the desktop."
    } catch {
        Write-Host "Jiraffe installation failed: $($_.Exception.Message)" -ForegroundColor Red
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Install-Jiraffe
