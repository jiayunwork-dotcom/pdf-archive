#!/usr/bin/env python3
import sys
import subprocess

try:
    from pypdf import PdfReader
except ImportError:
    subprocess.check_call([sys.executable, '-m', 'pip', 'install', 'pypdf', '-q'])
    from pypdf import PdfReader

for f in ['input/test_invoice.pdf', 'input/test_contract.pdf', 'input/test_resume.pdf']:
    print(f'\n{"="*60}')
    print(f'File: {f}')
    print("="*60)
    r = PdfReader(f)
    print(f'Pages: {len(r.pages)}')
    print(f'Metadata: {r.metadata}')
    for i, p in enumerate(r.pages):
        t = p.extract_text() or ''
        print(f'\nPage {i+1} text (len={len(t)}):')
        print(t)
