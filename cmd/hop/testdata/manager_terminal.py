import os, re, select, subprocess, sys, time, fcntl, struct, termios
master,slave=os.openpty()
fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',28,110,0,0))
initial=termios.tcgetattr(master)
p=subprocess.Popen([sys.argv[1],'-test.run=^TestManagerTerminalHelper$'],stdin=slave,stdout=slave,stderr=slave,env=dict(os.environ,MANAGER_TERMINAL_TEST='1',TERM='xterm-256color'),start_new_session=True)
os.close(slave)
data=bytearray();cursor=0
ansi=re.compile(r'\x1b\[[0-9;?]*[A-Za-z]')
def drain():
 if select.select([master],[],[],.05)[0]:
  try:
   chunk=os.read(master,65536)
   if not chunk:return False
   data.extend(chunk)
  except OSError:return False
 return True
def wait(text):
 global cursor
 target=text;end=time.monotonic()+15
 while time.monotonic()<end:
  plain=ansi.sub('',data.decode(errors='replace'))
  at=plain.find(target,cursor)
  if at>=0:cursor=at+len(target);return
  if not drain():break
 raise AssertionError(f'Missing {text}: {data.decode(errors="replace")}')
def ready():
 global cursor
 end=time.monotonic()+10
 while time.monotonic()<end:
  frame=bytes(data).decode(errors='replace').rsplit('\x1b[H',1)[-1]
  plain=ansi.sub('',frame)
  if 'LOCAL' in plain and 'REMOTE' in plain and 'Loading' not in plain and 'h Help' in plain:
   cursor=len(ansi.sub('',bytes(data).decode(errors='replace')));return
  if not drain():break
 raise AssertionError('panels did not finish loading')
try:
 ready()
 os.write(master,b'\x1b[200~D\r\x1b[201~');wait('Paste into a field')
 os.write(master,b'h');wait('Commands')
 os.write(master,b'\x1b');ready()
 os.write(master,b'o');wait('Options');wait('Preview only')
 os.write(master,b'j\r');wait('Preview only')
 os.write(master,b'\r\x1b');ready()
 assert b'.secret' not in data,'hidden files visible by default'
 os.write(master,b'\x08');wait('.secret')
 os.write(master,b'\x08');wait('Hidden files hidden')
 os.write(master,b'\x1b[B ');wait('1 marked')
 os.write(master,b'j ');wait('2 marked')
 os.write(master,b'\x1b');wait('0 marked');wait('Selection cleared')
 os.write(master,b'\t/private\x1bl');wait('Permission denied:')
 os.write(master,b'\x15\t')
 os.write(master,b'/alpha\x1b ');wait('1 marked')
 os.write(master,b' ');wait('0 marked')
 os.write(master,b' ');wait('1 marked')
 os.write(master,b'c');wait('Copy · LOCAL → REMOTE');wait('Copy complete')
 ready()
 os.write(master,b'\t/remote\x1b ');wait('1 marked')
 os.write(master,b'c');wait('Copy · REMOTE → LOCAL');wait('Copy complete')
 ready()
 os.write(master,b'\t/\x15discard\x1bdd');wait('Permanently delete')
 os.write(master,b'\x1b');wait('Delete cancelled')
 os.write(master,b'D');wait('Permanently delete')
 os.write(master,b'\r');wait('Delete complete');ready()
 os.write(master,b'\t/\x15discard\x1bD');wait('Permanently delete')
 os.write(master,b'\x1b[<0;8;25M');wait('Delete complete');ready()
 os.write(master,b'\t/\x15bundle\x1bm');wait('then remove sources?')
 os.write(master,b'\r');wait('Move complete');ready()
 os.write(master,b'/\x15replace\x1bm');wait('Replace existing files for this batch?')
 os.write(master,b'\x1b');wait('Operation cancelled')
 os.write(master,b'm');wait('Replace existing files for this batch?')
 os.write(master,b'\x1b[<0;8;25M');wait('then remove sources?')
 os.write(master,b'\r');wait('Move complete');ready()
 os.write(master,b'/\x15large\x1bc');wait('Copy complete');ready()
 assert b'this may take a while' not in data, 'large-copy warning still shown'
 os.write(master,b'\x15\x0e');wait('New folder:')
 os.write(master,b'new folder\r');wait('Folder created:')
 os.write(master,b'P\x15\x1b[200~/definitely-missing-hop-ux-path\x1b[201~\r');wait('Folder not found:')
 wait('PATH · Type a path');os.write(master,b'\x1b')
 os.write(master,b'\x03');wait('MANAGER_OK')
 assert p.wait(timeout=5)==0
 end=time.monotonic()+2
 while time.monotonic()<end and select.select([master],[],[],.1)[0]:
  if not drain():break
 assert b'MB/s effective' in data and b'ETA' in data, 'missing transfer rate or ETA'
 assert b'Review copy' not in data and b'[Y/n' not in data,'copy escaped the panels'
 for frame in bytes(data).split(b'\x1b[H'):
  if b'folders found' in frame or b'Copy complete' in frame:
   assert b'REMOTE' in frame and b'LOCAL' in frame,'transfer hid a pane'
 assert termios.tcgetattr(master)==initial,'terminal left raw'
 assert data.count(b'\x1b[?1049h')==1,'fullscreen restarted between panels/transfers'
 exits=[i for i in range(len(data)) if data.startswith(b'\x1b[?1049l',i)]
 assert len(exits)==1, f'fullscreen exited {len(exits)} times at {[bytes(data[max(0,i-50):i+80]) for i in exits]}'
finally:
 if p.poll() is None:p.kill();p.wait()
 os.close(master)
