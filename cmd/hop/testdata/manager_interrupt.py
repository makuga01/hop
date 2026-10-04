import os, select, subprocess, sys, time, fcntl, struct, termios, signal
for via_signal in (False, True, "escape"):
 master,slave=os.openpty()
 fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',28,110,0,0))
 initial=termios.tcgetattr(master)
 p=subprocess.Popen([sys.argv[1],'-test.run=^TestManagerDockInterruptHelper$'],stdin=slave,stdout=slave,stderr=slave,env=dict(os.environ,MANAGER_DOCK_INTERRUPT='1',MANAGER_STOP_MODE=str(via_signal),TERM='xterm-256color'),start_new_session=True)
 os.close(slave);data=bytearray()
 def wait(text):
  end=time.monotonic()+10
  while time.monotonic()<end:
   if text.encode() in data:return
   if select.select([master],[],[],.1)[0]:
    try:data.extend(os.read(master,65536))
    except OSError:break
  raise AssertionError(f'missing {text}: {data.decode(errors="replace")}')
 try:
  wait('alpha.txt')
  end=time.monotonic()+10
  while time.monotonic()<end:
   frame=bytes(data).rsplit(b'\x1b[H',1)[-1]
   if b'Loading' not in frame and b'h Help' in frame:break
   if select.select([master],[],[],.1)[0]:data.extend(os.read(master,65536))
  else:raise AssertionError('panes not ready')
  os.write(master,b'/alpha\x1bc');wait('folders found')
  os.write(master,b'\r');time.sleep(.2)
  assert p.poll() is None,'Enter cancelled a running copy'
  if via_signal == "escape":os.write(master,b'\x1b')
  elif via_signal:p.send_signal(signal.SIGINT)
  else:
   os.write(master,b'\x03')
  wait('DOCK_INTERRUPTED');assert p.wait(timeout=5)==0
  assert termios.tcgetattr(master)==initial,'terminal left raw after cancellation'
 finally:
  if p.poll() is None:p.kill();p.wait()
  os.close(master)
