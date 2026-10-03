"""Check diagnostic counts and terminal cleanup without capturing user input."""
import os,sys,fcntl,struct,subprocess,termios,select,time
master,slave=os.openpty()
fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',28,110,0,0))
initial=termios.tcgetattr(master)
p=subprocess.Popen([sys.argv[1],'-test.run=^TestMouseDiagnosticHelper$'],stdin=slave,stdout=slave,stderr=slave,env=dict(os.environ,HOP_DIAGNOSTIC_TEST='1',TERM='xterm-256color'),start_new_session=True)
os.close(slave)
data=bytearray()
try:
 deadline=time.monotonic()+10
 while b'Click and scroll anywhere' not in data:
  if time.monotonic()>deadline:raise AssertionError('diagnostic did not open')
  if select.select([master],[],[],.1)[0]:data.extend(os.read(master,65536))
 os.write(master,b'\x1b[<0;10;10M\x1b[<0;10;10m\x1b[<65;10;10M1')
 os.write(master,bytes([27,91,77,32,42,42,27,91,77,96,42,42]))
 os.write(master,b'private-typed-fixtureq')
 while p.poll() is None:
  if time.monotonic()>deadline:raise AssertionError('diagnostic did not exit')
  if select.select([master],[],[],.1)[0]:
   try:data.extend(os.read(master,65536))
   except OSError:break
 assert p.wait(timeout=5)==0,data.decode(errors='replace')
 assert b'Basic: clicks=1 scroll=1 drag=0 SGR=false legacy=true' in data,data.decode(errors='replace')
 assert b'Drag:  clicks=1 scroll=1 drag=0 SGR=true legacy=false' in data,data.decode(errors='replace')
 assert b'private-typed-fixture' not in data,'typed text leaked'
 assert termios.tcgetattr(master)==initial,'terminal not restored'
finally:
 if p.poll() is None:p.kill();p.wait()
 os.close(master)
