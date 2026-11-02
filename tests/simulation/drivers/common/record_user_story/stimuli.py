"""Deterministic synthetic native inputs; no private files or network assets."""
import base64
import struct
import zlib


def png():
    def chunk(kind, data):
        return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data))
    pixels = b''.join(b'\0' + bytes([32, 96, 224]) * 64 for _ in range(64))
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 64, 64, 8, 2, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(pixels)) + chunk(b'IEND', b'')


def pdf():
    stream = b'BT /F1 18 Tf 40 200 Td (SYNTHETIC DOCUMENT 42) Tj ET'
    objects = [b'<< /Type /Catalog /Pages 2 0 R >>', b'<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
        b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>',
        b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>', b'<< /Length ' + str(len(stream)).encode() + b' >>\nstream\n' + stream + b'\nendstream']
    data = b'%PDF-1.4\n'; offsets = [0]
    for i, obj in enumerate(objects, 1):
        offsets.append(len(data)); data += str(i).encode() + b' 0 obj\n' + obj + b'\nendobj\n'
    start = len(data)
    data += b'xref\n0 6\n0000000000 65535 f \n' + b''.join(f'{n:010} 00000 n \n'.encode() for n in offsets[1:])
    return data + b'trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n' + str(start).encode() + b'\n%%EOF\n'


def content(provider, prompt, kind=None):
    items = [{'type': 'text', 'text': prompt}]
    if kind in ('image', 'mixed'):
        encoded = base64.b64encode(png()).decode()
        items.append({'type': 'image', 'url': 'data:image/png;base64,' + encoded} if provider == 'codex' else
                     {'type': 'image', 'source': {'type': 'base64', 'media_type': 'image/png', 'data': encoded}})
    if kind == 'document':
        if provider != 'claude':
            raise ValueError('Codex has no native document input variant')
        items.append({'type': 'document', 'source': {'type': 'base64', 'media_type': 'application/pdf', 'data': base64.b64encode(pdf()).decode()}})
    if kind == 'mixed':
        items.append({'type': 'text', 'text': 'This final text block belongs to the same mixed input.'})
    return items
