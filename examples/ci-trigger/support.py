"""Locate the shared Python client of the Go lifecycle host. This is application wiring, not a copied runtime."""
import os
from pathlib import Path
import sys
LIFE=Path(os.environ.get('LIFECYCLE_CLIENT_SOURCE',Path(__file__).resolve().parents[2]/'src/lifecycle-core/client')).resolve()
sys.path.insert(0,str(LIFE))
from lifecycle_client import Fault,Driver,READS,MIMES,encode,decode,content_ref,envelope,media,handle,read_store,record,root,host_sources
ROOT=Path(__file__).resolve().parent

def fixture():return decode((ROOT/'fixtures/workflow.json').read_bytes())
