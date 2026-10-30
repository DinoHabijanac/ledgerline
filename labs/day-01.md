docker stats:

CONTAINER ID   NAME                       CPU %      MEM USAGE / LIMIT   MEM %     NET I/O           BLOCK I/O        PIDS
573846adb048   ledgerline-control-plane   2198.40%   695MiB / 4.804GiB   14.13%    2.84kB / 1.33kB   67.2MB / 754MB   274


deleting the pod (kubectl -n ledgerline delete pod <name>)
-when pod is deleted, Kubernetes sends `SIGTERM` signal to container, grace period is started and application doesnt accept new connections and finishes active requests safely
-Kubernetes ctl plane detects termination, schedules a replacement pod on worker node

readiness vs liveness probe checks
- if liveness probe checks external dependency (ex. database) temp network glitch or slow database cause it to fail
- Kubernetes kills and restarts container, even if unnecessary
- liveness probe should check if local service process is alive
- external health should be handled by readiness probe

memory limit set, CPU limit NOT set
- memory limit: if container goes over it is terminated but node is protected
- CPU limit: flexible, app slows down not crashes; no limit = borrow extra system power when free

why not to log probe requests
-probe requests have frequent logs, causes log ingestion which costs and buries critical business telemetry and real errors

