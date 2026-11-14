param([string]$file, [string]$pattern, [int]$ctx = 300, [int]$max = 20)
$text = [System.IO.File]::ReadAllText($file)
$ms = [regex]::Matches($text, $pattern)
Write-Host "== $file pattern=$pattern total=$($ms.Count)"
$n = 0
foreach ($m in $ms) {
  if ($n -ge $max) { break }
  $s = [Math]::Max(0, $m.Index - $ctx)
  $len = [Math]::Min($text.Length - $s, $m.Length + 2*$ctx)
  Write-Host ("---- match#{0} @ {1}" -f $n, $m.Index)
  Write-Host $text.Substring($s, $len)
  $n++
}
