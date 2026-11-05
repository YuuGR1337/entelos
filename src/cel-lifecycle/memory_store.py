"""In-memory storage adapter for the storage port. It keeps no data after the process stops."""
import threading
from git_store import Fault,View,encode,decode,media,envelope,content_ref,empty_state

class MemoryStore:
    """Keep accepted revisions in process memory. Each revision has one parent, a state and content entries."""
    def __init__(self):
        self.lock=threading.Lock()
        self.head=''
        self.revisions={}  # cursor -> (parent cursor, encoded state, entries)
        self.blobs={}
    def blob(self,oid):
        if oid not in self.blobs:raise Fault('store_error','missing content entry','store')
        return self.blobs[oid]
    def root(self):
        with self.lock:return self.head
    def history(self,cursor):
        result=[]
        while cursor:
            result.append(cursor);cursor=self.revisions[cursor][0]
        return result
    def read(self,cursor=None):
        if cursor is None:cursor=self.root()
        if not isinstance(cursor,str) or (cursor and cursor not in self.revisions):
            raise Fault('unknown_cursor','cursor is not in accepted history','store')
        if not cursor:return View(self,'',empty_state(),{},{})
        _,state,entries=self.revisions[cursor]
        # Decode a new copy, so that a change to a view cannot change an accepted revision.
        return View(self,cursor,decode(state),dict(entries),{})
    def transact(self,expected,key,payload,apply):
        if not isinstance(key,str) or not key or len(key)>128:raise Fault('invalid_request','bounded request key required')
        view=self.read();digest=content_ref(envelope(media('request'),encode(payload)))
        previous=view.state['requests'].get(key)
        if previous:
            if previous['payload_ref']!=digest:raise Fault('idempotency_conflict','key was used for another payload')
            origin=view.cursor
            for cursor in self.history(view.cursor):
                if key not in self.read(cursor).state['requests']:break
                origin=cursor
            return {'cursor':origin,'result':previous['result'],'reused':True}
        if expected!=view.cursor:raise Fault('stale_revision','capture the current root and re-evaluate')
        # Apply runs without the lock. The final compare-and-swap below rejects a writer that lost a race.
        result=apply(view)
        view.state['requests'][key]={'payload_ref':digest,'result':result}
        state=encode(view.state)
        entries=dict(view.entries)
        for ref in view.pending:entries['o-'+ref[7:]]=ref
        with self.lock:
            if self.head!=expected:raise Fault('stale_revision','root CAS lost; no state/outbox was published')
            cursor='mem:%d'%(len(self.revisions)+1)
            for ref,body in view.pending.items():self.blobs[ref]=body
            self.revisions[cursor]=(expected,state,entries)
            self.head=cursor
        return {'cursor':cursor,'result':result,'reused':False}
