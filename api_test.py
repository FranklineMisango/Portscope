import asyncio, json, os
import websockets
from dotenv import load_dotenv
load_dotenv()

API_KEY = os.getenv("AISSTREAM_API_KEY")
async def stream():
    async with websockets.connect(
        "wss://stream.aisstream.io/v0/stream",
        compression="deflate",
    ) as ws:
        await ws.send(json.dumps({
            "APIKey": API_KEY,
            "BoundingBoxes": [[[25.835, -80.208], [25.603, -79.879]]],
            "FilterMessageTypes": ["StaticDataReport"]
        }))
        async for payload in ws:
            print(json.loads(payload))

asyncio.run(stream())