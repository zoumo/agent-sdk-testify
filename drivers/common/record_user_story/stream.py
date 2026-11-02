"""Read native JSON lines without asyncio's default 64 KiB line limit."""
import json


async def json_messages(reader):
    pending = bytearray()
    while chunk := await reader.read(65536):
        pending.extend(chunk)
        while b'\n' in pending:
            line, _, rest = pending.partition(b'\n')
            pending = bytearray(rest)
            yield json.loads(line)
    if pending:
        raise ValueError('incomplete native JSON frame')
