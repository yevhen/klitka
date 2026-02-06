param(
  [string]$Archive = ""
)

if (-not $Archive) {
  $Archive = Get-ChildItem -Path dist -Filter "klitka_*_windows_amd64.zip" | Select-Object -First 1
}
if (-not $Archive) {
  throw "no archive found for smoke test"
}

$workDir = Join-Path $env:TEMP ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Force -Path $workDir | Out-Null

Expand-Archive -Path $Archive.FullName -DestinationPath $workDir -Force
$rootDir = Get-ChildItem -Path $workDir -Directory | Where-Object { $_.Name -like "klitka_*" } | Select-Object -First 1
if (-not $rootDir) {
  throw "archive did not contain expected root directory"
}

$env:PATH = "$($rootDir.FullName);$env:PATH"
if (-not $env:KLITKA_WSL_DISTRO) {
  $env:KLITKA_WSL_DISTRO = "Ubuntu"
}

$cli = Join-Path $rootDir.FullName "klitka.exe"

& $cli exec -- "uname" "-a"
"echo hi`nexit`n" | & $cli shell
