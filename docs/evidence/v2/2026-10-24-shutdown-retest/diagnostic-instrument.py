from pathlib import Path
import json
root=Path.cwd(); scratch=root/'.cache/shutdown-retest'
connection=(root/'internal/relay/connection.go').read_text()
connection=connection.replace('"context"','"context"\n "encoding/json"\n "errors"\n "fmt"\n "log"')
connection=connection.replace('type client struct {','type client struct {\n diagnosticReadEnd atomic.Pointer[shutdownReadResult]\n diagnosticGeneration uint64')
connection=connection.replace('c.server.metrics.writeErrors.Add(1)', '''c.server.metrics.writeErrors.Add(1)
        deadline, _ := ctx.Deadline()
        c.traceShutdown("write_error", map[string]any{"body": fmt.Sprintf("%T", envelope.Body), "sequence": envelope.Sequence, "startedNs": started.UnixNano(), "deadlineNs": deadline.UnixNano(), "deadlineElapsed": !time.Now().Before(deadline), "writeContextError": fmt.Sprint(ctx.Err()), "error": err.Error(), "isDeadline": errors.Is(err, context.DeadlineExceeded), "isCanceled": errors.Is(err, context.Canceled), "closeStatus": websocket.CloseStatus(err), "readEnd": c.diagnosticReadEnd.Load(), "terminal": c.terminal.Load()})''')
needle='kind, data, err := c.conn.Read(ctx)\n\t\tcancel()\n\t\tif err != nil {\n\t\t\treturn'
replacement='''kind, data, err := c.conn.Read(ctx)
        cancel()
        if err != nil {
            result := &shutdownReadResult{AtNs: time.Now().UnixNano(), Error: err.Error(), CloseStatus: int(websocket.CloseStatus(err))}
            c.diagnosticReadEnd.Store(result)
            c.traceShutdown("read_end", map[string]any{"readEnd": result})
            return'''
assert needle in connection
connection=connection.replace(needle,replacement)
connection=connection.replace('if c.terminal.CompareAndSwap(nil, &closeRequest{code: code, status: status, reason: reason}) {','if c.terminal.CompareAndSwap(nil, &closeRequest{code: code, status: status, reason: reason}) {\n c.traceShutdown("terminal_requested", map[string]any{"code":code,"status":status,"reason":reason})')
connection+='''
type shutdownReadResult struct {
 AtNs int64 `json:"atNs"`
 Error string `json:"error"`
 CloseStatus int `json:"closeStatus"`
}
func (c *client) traceShutdown(event string, fields map[string]any) {
 fields["event"], fields["atNs"], fields["connection"] = event, time.Now().UnixNano(), fmt.Sprintf("%p", c)
 fields["parentContextError"] = fmt.Sprint(c.ctx.Err())
 fields["generation"] = c.diagnosticGeneration
 if c.member != nil { fields["participant"] = c.member.id }
 if c.room != nil { fields["roomInstance"] = c.room.instanceID }
 encoded, _ := json.Marshal(fields)
 log.Printf("SHUTDOWN %s", encoded)
}
'''
server=(root/'internal/relay/server.go').read_text()
server=server.replace('authenticating = false\n\treleaseAuthentication()', 'c.diagnosticGeneration = uint64(welcome.Generation)\n authenticating = false\n\treleaseAuthentication()')
needle='go func() { defer close(writerDone); c.writeLoop(); c.cancel() }()\n\tc.readLoop()\n\tc.cancel()'
assert needle in server
server=server.replace(needle,'go func() { defer close(writerDone); c.writeLoop(); c.traceShutdown("cancel", map[string]any{"origin":"writer_return"}); c.cancel() }()\n\tc.readLoop()\n c.traceShutdown("cancel", map[string]any{"origin":"reader_return"})\n\tc.cancel()')
load=(root/'cmd/relay-load/main.go').read_text().replace('"io"','"io"\n "log"')
load=load.replace('r.publish(ctx, conn)', 'r.publish(ctx, conn, index)').replace('r.read(ctx, conn, c.slowMs > 0 && index%c.roomSize == 0, &begin)', 'r.read(ctx, conn, c.slowMs > 0 && index%c.roomSize == 0, &begin, index)')
load=load.replace('func (r *runner) publish(ctx context.Context, conn *websocket.Conn)', 'func (r *runner) publish(ctx context.Context, conn *websocket.Conn, index int)')
load=load.replace('func (r *runner) read(ctx context.Context, conn *websocket.Conn, slow bool, begin *atomic.Int64)', 'func (r *runner) read(ctx context.Context, conn *websocket.Conn, slow bool, begin *atomic.Int64, index int)')
needle='\tcancel()\n\twork.Wait()'
assert needle in load
load=load.replace(needle,'\tloadShutdownTrace("cancel_all", map[string]any{})\n\tcancel()\n\twork.Wait()\n loadShutdownTrace("workers_stopped", map[string]any{})')
load=load.replace('func (r *runner) cleanup() {','func (r *runner) cleanup() {\n loadShutdownTrace("cleanup_begin", map[string]any{})')
needle='_, data, err := conn.Read(ctx)\n\t\tif err != nil {'
assert needle in load
load=load.replace(needle,'_, data, err := conn.Read(ctx)\n\t\tif err != nil {\n loadShutdownTrace("read_end", map[string]any{"participant":fmt.Sprintf("p-%d",index), "contextError":fmt.Sprint(ctx.Err()), "error":err.Error(), "closeStatus":websocket.CloseStatus(err)})')
needle='\t\tif err != nil {\n\t\t\tif ctx.Err() == nil {\n\t\t\t\tr.failure(err)'
# The first occurrence is the publisher; the reader was instrumented above.
assert needle in load
load=load.replace(needle,'\t\tif err != nil {\n loadShutdownTrace("publish_error", map[string]any{"participant":fmt.Sprintf("p-%d",index), "contextError":fmt.Sprint(ctx.Err()), "error":err.Error()})\n\t\t\tif ctx.Err() == nil {\n\t\t\t\tr.failure(err)',1)
load+='''
func loadShutdownTrace(event string, fields map[string]any) {
 fields["event"], fields["atNs"] = event, time.Now().UnixNano()
 encoded, _ := json.Marshal(fields)
 log.Printf("LOAD_SHUTDOWN %s", encoded)
}
'''
replacements={}
for name, content, relative in [('connection-baseline.go',connection,'internal/relay/connection.go'),('server-baseline.go',server,'internal/relay/server.go'),('load-baseline.go',load,'cmd/relay-load/main.go')]:
 target=scratch/name;target.write_text(content);replacements[str(root/relative)]=str(target)
(scratch/'overlay-baseline.json').write_text(json.dumps({'Replace':replacements},indent=2)+'\n')
