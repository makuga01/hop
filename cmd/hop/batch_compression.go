package main

import (
	"compress/gzip"
	"errors"
	"io"

	"github.com/klauspost/compress/zstd"
)

// Prefer an already available Zstandard implementation. Nothing is installed
// remotely. Older Python hosts without zstd retain the gzip stream.
// A 64 MiB history matches repeated blocks across large files. Bound the decoder
// window explicitly as the helper is an external, potentially untrusted process.
const batchCompressionPython = `import sys,gzip,shutil,subprocess,threading,atexit
class NativeZstdOutput:
 magic=b'HOPFILEZ1\n'
 def __init__(self):
  from compression.zstd import ZstdCompressor,CompressionParameter as P
  self.c=ZstdCompressor(options={P.compression_level:9,P.window_log:26,P.enable_long_distance_matching:1})
 def start(self): pass
 def write(self,data): sys.stdout.buffer.write(self.c.compress(data))
 def flush(self):
  sys.stdout.buffer.write(self.c.flush(self.c.FLUSH_BLOCK));sys.stdout.buffer.flush()
 def close(self):
  sys.stdout.buffer.write(self.c.flush());sys.stdout.buffer.flush()
class CommandZstdOutput:
 magic=b'HOPFILEZ1\n'
 def __init__(self,binary):
  self.p=subprocess.Popen([binary,'-q','-9','--long=26','-c'],stdin=subprocess.PIPE,stdout=subprocess.PIPE)
  self.error=None
  self.t=threading.Thread(target=self.relay,daemon=True)
  atexit.register(self.abort)
 def abort(self):
  if self.p.poll() is None: self.p.terminate()
  try: self.p.wait(timeout=1)
  except subprocess.TimeoutExpired:
   self.p.kill();self.p.wait()
  if self.t.is_alive(): self.t.join(timeout=1)
 def relay(self):
  try:
   while True:
    data=self.p.stdout.read1(65536)
    if not data: break
    sys.stdout.buffer.write(data);sys.stdout.buffer.flush()
  except BaseException as e:
   self.error=e;self.p.terminate()
 def start(self): self.t.start()
 def write(self,data): self.p.stdin.write(data)
 def flush(self): self.p.stdin.flush()
 def close(self):
  try: self.p.stdin.close()
  finally:
   code=self.p.wait();self.t.join();self.p.stdout.close()
  if code or self.error is not None: raise RuntimeError('zstd compressor failed')
def batch_output():
 try: out=NativeZstdOutput()
 except ImportError:
  binary=shutil.which('zstd')
  try: out=CommandZstdOutput(binary) if binary else None
  except OSError: out=None
  if out is None:
   sys.stdout.buffer.write(b'HOPFILES1\n');sys.stdout.buffer.flush()
   return gzip.GzipFile(fileobj=sys.stdout.buffer,mode='wb',compresslevel=1)
 sys.stdout.buffer.write(out.magic);sys.stdout.buffer.flush();out.start()
 return out
`

func batchDecompressor(magic string, input io.Reader) (io.ReadCloser, error) {
	switch magic {
	case "HOPFILES1\n":
		return gzip.NewReader(input)
	case "HOPFILEZ1\n":
		decoder, err := zstd.NewReader(input, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(128<<20), zstd.WithDecoderMaxWindow(64<<20))
		if err != nil {
			return nil, err
		}
		return decoder.IOReadCloser(), nil
	default:
		return nil, errors.New("unknown batch compression")
	}
}
