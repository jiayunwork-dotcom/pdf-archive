#!/usr/bin/env python3
import sys
import subprocess

try:
    from reportlab.lib.pagesizes import A4
    from reportlab.pdfgen import canvas
    from reportlab.lib.units import mm
    from reportlab.pdfbase import pdfmetrics
    from reportlab.pdfbase.cidfonts import UnicodeCIDFont
except ImportError:
    print("Installing reportlab...")
    subprocess.check_call([sys.executable, '-m', 'pip', 'install', 'reportlab', '-q'])
    from reportlab.lib.pagesizes import A4
    from reportlab.pdfgen import canvas
    from reportlab.lib.units import mm

try:
    pdfmetrics.registerFont(UnicodeCIDFont('STSong-Light'))
    FONT_CN = 'STSong-Light'
except:
    FONT_CN = 'Helvetica'

def create_invoice():
    c = canvas.Canvas('input/test_invoice.pdf', pagesize=A4)
    c.setFont(FONT_CN, 20)
    c.drawString(50*mm, 270*mm, 'VAT Invoice')
    c.setFont(FONT_CN, 12)
    c.drawString(50*mm, 260*mm, 'Invoice No: 12345678')
    c.drawString(50*mm, 255*mm, 'Invoice Code: 011002300111')
    c.drawString(50*mm, 250*mm, 'Date: 2024-03-15')
    c.drawString(50*mm, 240*mm, 'Seller: Beijing Tech Co Ltd')
    c.drawString(50*mm, 235*mm, 'Tax ID: 91110000MA001ABCDE')
    c.drawString(50*mm, 225*mm, 'Buyer: Shanghai Trading Co Ltd')
    c.drawString(50*mm, 220*mm, 'Tax ID: 91310000MA009XYZ12')
    c.drawString(50*mm, 180*mm, 'Total (capital): YI WAN YI QIAN SAN BAI YUAN')
    c.drawString(50*mm, 175*mm, 'Total: CNY 11,300.00')
    c.drawString(50*mm, 170*mm, 'Amount: 10,000.00')
    c.drawString(50*mm, 165*mm, 'Tax: 1,300.00')
    c.save()
    print('Created test_invoice.pdf')

def create_contract():
    c = canvas.Canvas('input/test_contract.pdf', pagesize=A4)
    c.setFont(FONT_CN, 18)
    c.drawString(50*mm, 270*mm, 'Service Contract')
    c.setFont(FONT_CN, 12)
    c.drawString(50*mm, 260*mm, 'Contract No: HT20240301001')
    c.drawString(50*mm, 250*mm, 'Party A: Beijing Tech Co Ltd')
    c.drawString(50*mm, 245*mm, 'Party B: Shanghai Trading Co Ltd')
    c.drawString(50*mm, 235*mm, 'Sign Date: 2024-03-01')
    c.drawString(50*mm, 225*mm, 'Contract Amount: CNY 500,000.00')
    c.drawString(50*mm, 200*mm, 'Article 1 Service Scope')
    c.drawString(50*mm, 195*mm, 'Party A entrusts Party B with software development...')
    c.drawString(50*mm, 180*mm, 'Article 12 Breach of Contract')
    c.drawString(50*mm, 175*mm, 'Both parties shall strictly fulfill rights and obligations...')
    c.save()
    print('Created test_contract.pdf')

def create_resume():
    c = canvas.Canvas('input/test_resume.pdf', pagesize=A4)
    c.setFont(FONT_CN, 20)
    c.drawString(50*mm, 275*mm, 'Personal Resume')
    c.setFont(FONT_CN, 12)
    c.drawString(50*mm, 265*mm, 'Name: Zhang San')
    c.drawString(50*mm, 260*mm, 'Phone: 13800138000')
    c.drawString(50*mm, 255*mm, 'Email: zhangsan@example.com')
    c.setFont(FONT_CN, 14)
    c.drawString(50*mm, 240*mm, 'Education')
    c.setFont(FONT_CN, 12)
    c.drawString(50*mm, 230*mm, '2018-2022 Tsinghua University Computer Science Bachelor')
    c.setFont(FONT_CN, 14)
    c.drawString(50*mm, 215*mm, 'Work Experience')
    c.setFont(FONT_CN, 12)
    c.drawString(50*mm, 205*mm, '2022-Present ABC Tech Senior Engineer')
    c.setFont(FONT_CN, 14)
    c.drawString(50*mm, 185*mm, 'Skills')
    c.setFont(FONT_CN, 12)
    c.drawString(50*mm, 175*mm, 'Go, Python, Java, Kubernetes, Docker')
    c.save()
    print('Created test_resume.pdf')

create_invoice()
create_contract()
create_resume()
print('All test PDFs created!')
