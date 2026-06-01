import asyncio
import websockets
import json
from datetime import datetime, timezone
from clickhouse_driver import Client
import os
from pathlib import Path


def load_api_key() -> str:
    api_key = os.environ.get("AISAPIKEY")
    if api_key:
        return api_key

    env_file = Path(__file__).with_name(".env")
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            if key.strip() == "AISAPIKEY":
                api_key = value.strip().strip('"').strip("'")
                if api_key:
                    os.environ["AISAPIKEY"] = api_key
                    return api_key

    raise RuntimeError("AISAPIKEY is not set. Export it or add it to ais_stream_tests/.env before running this script.")


ch_user = os.environ.get("CLICKHOUSE_USER")
ch_password = os.environ.get("CLICKHOUSE_PASSWORD")

if not ch_user or not ch_password:
    env_file = Path(__file__).with_name(".env")
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            value = value.strip().strip('"').strip("'")
            if key == "CLICKHOUSE_USER" and not ch_user:
                ch_user = value
            elif key == "CLICKHOUSE_PASSWORD" and not ch_password:
                ch_password = value

if not ch_user or not ch_password:
    raise RuntimeError("CLICKHOUSE_USER and CLICKHOUSE_PASSWORD must be set in .env or environment")

client = Client(host='localhost', user=ch_user, password=ch_password)

# Create database and table
client.execute('CREATE DATABASE IF NOT EXISTS vessels_tracking')

create_table_query = '''
CREATE TABLE IF NOT EXISTS vessels_tracking.ais_data (
    ts DateTime64(3, 'UTC'),
    ship_id UInt32,
    latitude Float32,
    longitude Float32,
    speed Float32,
    heading Float32,
    nav_status String
) ENGINE = MergeTree()
ORDER BY ts;
'''

client.execute(create_table_query)

# Connect to AIS stream and insert data into ClickHouse
async def connect_ais_stream():
    api_key = load_api_key()

    async with websockets.connect("wss://stream.aisstream.io/v0/stream") as websocket:
        subscribe_message = {
            "APIKey": api_key,  # Required!
            "BoundingBoxes": [
                # Buenos Aires, Argentina
                [[-34.811548, -58.537903], [-34.284453, -57.749634]],
                # San Francisco, USA
                [[36.989391, -123.832397], [38.449287, -121.744995]],
            ],
            "FilterMessageTypes": ["PositionReport"],
        }

        try:
            await websocket.send(json.dumps(subscribe_message))

            async for message_json in websocket:
                message = json.loads(message_json)
                message_type = message["MessageType"]

                if message_type == "PositionReport":
                    # The message parameter contains a key of the message type which contains the message itself
                    ais_message = message["Message"]["PositionReport"]
                    print(f"[{datetime.now(timezone.utc)}] ShipId: {ais_message['UserID']} Latitude: {ais_message['Latitude']} Longitude: {ais_message['Longitude']} Speed: {ais_message['Sog']} Heading: {ais_message['Cog']} NavStatus: {ais_message['NavigationalStatus']}")
                    # Insert data into ClickHouse
                    insert_query = '''
                    INSERT INTO vessels_tracking.ais_data (ts, ship_id, latitude, longitude, speed, heading, nav_status) VALUES
                    '''
                    # Ensure nav_status is a string
                    values = (
                        datetime.now(timezone.utc),
                        ais_message['UserID'],
                        ais_message['Latitude'],
                        ais_message['Longitude'],
                        ais_message['Sog'],
                        ais_message['Cog'],
                        str(ais_message['NavigationalStatus'])  # Cast to string
                    )
                    client.execute(insert_query, [values])
        except websockets.exceptions.ConnectionClosedError as exc:
            raise RuntimeError(
                "AIS stream connection closed unexpectedly. Verify the API key and subscription payload, then retry."
            ) from exc

if __name__ == "__main__":
    asyncio.run(connect_ais_stream())