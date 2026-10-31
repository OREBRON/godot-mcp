# Windows version of kill-test-processes.sh: ends Godot and godot-mcp
# processes whose command line points under RUNNER_TEMP.
$temp = $env:RUNNER_TEMP
if (-not $temp) { throw 'RUNNER_TEMP is not set' }
Get-CimInstance Win32_Process |
    Where-Object { $_.Name -like 'godot*' -and $_.CommandLine -and $_.CommandLine.Contains($temp) } |
    ForEach-Object {
        Write-Host "ending $($_.Name) (pid $($_.ProcessId))"
        Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
    }
exit 0
