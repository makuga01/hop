import os, select, subprocess, sys, time
master,slave=os.openpty()
p=subprocess.Popen([sys.argv[1],'-test.run=^TestSpaceSelectionHelper$'],stdin=slave,stdout=slave,stderr=slave,env=dict(os.environ,SPACE_SELECTION_TEST='1',TERM='xterm-256color'),start_new_session=True)
os.close(slave)
data=bytearray();cursor=0
def wait(text):
 global cursor
 target=text.encode();deadline=time.monotonic()+10
 while time.monotonic()<deadline:
  at=data.find(target,cursor)
  if at>=0:cursor=at+len(target);return
  if select.select([master],[],[],.1)[0]:
   try:data.extend(os.read(master,65536))
   except OSError:break
 raise AssertionError(f'Missing {text}: {data.decode(errors="replace")}')
try:
 wait('beta.txt')
 os.write(master,b' ');wait('1 marked')
 os.write(master,b' ');wait('0 marked')
 os.write(master,b' ');wait('1 marked')
 os.write(master,b'\t');wait('Review selected files')
 os.write(master,b'\r');wait('SPACE_STAYS_OK')
 assert p.wait(timeout=5)==0
finally:
 if p.poll() is None:p.kill();p.wait()
 os.close(master)
