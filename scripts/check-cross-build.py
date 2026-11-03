"""Compile target binaries with CGO disabled; does not execute foreign binaries."""
import os,subprocess,tempfile
from pathlib import Path
with tempfile.TemporaryDirectory(prefix='tcsim-cross-') as directory:
 for target,arch in [('linux','amd64'),('linux','arm64'),('darwin','amd64'),('darwin','arm64'),('windows','amd64')]:
  output=Path(directory)/(target+'-'+arch)
  subprocess.run([os.environ.get('GO','go'),'build','-trimpath','-ldflags=-s -w','-o',str(output),'./cmd/simulator'],env={**os.environ,'GOOS':target,'GOARCH':arch,'CGO_ENABLED':'0'},check=True)
  print(target+'/'+arch+': built',flush=True)
