ReadHeaderTimeout
- timeout for reading request header
- if 0 ReadTimeout value
- if <0 no timeout

srv.Shutdown
- gracefully shut down the server without interrupting active connections
- works by: close all open listeners, then close all idle connections

srv.Close
- immediately close all active listeners and connections with StateNew,StateActive,StateIdle
- doesn't close hijacked connections

Why ignore http.ErrServerClosed:
- it is returned intentionally when the server is shut down cleanly, signaling a normal, expected shutdown rather than an unexpected error or crash.

signal.NotifyContext
- copy of parent context marked with done (Done=channel closed)
    - returns it when signal arrives, stop function is called or parents context Doone channel is closed
- stop function unregisters signal behavior
- when operations running in Context complete and signals dont need to be diverted to context, code should call stop asap

