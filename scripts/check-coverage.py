"""Enforce the repository's 90% floor for the full Go module, including the CLI.
The profile comes from race-enabled tests; this script does not filter files.
"""
import os,re,subprocess,sys
profile=sys.argv[1] if len(sys.argv)>1 else 'reports/coverage.out'
result=subprocess.run([os.environ.get('GO','go'),'tool','cover','-func='+profile],capture_output=True,text=True,check=True)
match=re.search(r'^total:.*?([0-9.]+)%\s*$',result.stdout,re.MULTILINE)
assert match,'Go did not report total coverage'
coverage=float(match[1]);print(f'Full-module Go statement coverage: {coverage:.1f}% (minimum 90.0%)')
if coverage<90.0:raise SystemExit('Coverage is below the repository minimum.')

# Cobertura measures source lines rather than Go statement blocks. The repository
# rule consumes this exact unfiltered report, so enforce both metrics locally.
if len(sys.argv)>2:
 import xml.etree.ElementTree as ET
 root=ET.parse(sys.argv[2]).getroot()
 covered=int(root.attrib['lines-covered']); valid=int(root.attrib['lines-valid'])
 assert valid>0, 'Cobertura report contains no source lines'
 print(f'Full-module source line coverage: {covered}/{valid} ({100*covered/valid:.2f}%; minimum 90%)')
 if covered*100<valid*90:raise SystemExit('Source line coverage is below the repository minimum.')
