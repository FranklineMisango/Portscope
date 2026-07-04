(function () {
	const e = React.createElement;
	const API_BASE = window.PORTSCOPE_API_BASE || 'http://127.0.0.1:8080';

	function apiUrl(path) {
		return `${API_BASE}${path}`;
	}

	function wsUrl(path) {
		const url = new URL(path, API_BASE);
		url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
		return url.toString();
	}

	function formatCount(value) {
		const number = Number(value);
		return Number.isFinite(number) ? number.toLocaleString() : 'n/a';
	}

	function formatPercent(value) {
		const number = Number(value);
		return Number.isFinite(number) ? `${number.toFixed(1)}%` : 'n/a';
	}

	function formatTime(value) {
		if (!value) return 'Unknown';
		try {
			return new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
		} catch (e) {
			return String(value);
		}
	}

	function pointFromGeometry(geom) {
		if (!geom) return null;
		const parsed = typeof geom === 'string' ? JSON.parse(geom) : geom;
		if (parsed && parsed.type === 'Point' && Array.isArray(parsed.coordinates) && parsed.coordinates.length >= 2) {
			const [lon, lat] = parsed.coordinates;
			if (Number.isFinite(lon) && Number.isFinite(lat)) return [lon, lat];
		}
		return null;
	}

	function formatPortLabel(port) {
		const bits = [port.name || 'Unnamed port'];
		if (port.country) bits.push(`(${port.country})`);
		if (port.vessel_count_total !== undefined && port.vessel_count_total !== null) {
			bits.push(`vessels: ${Number(port.vessel_count_total).toLocaleString()}`);
		}
		return bits.join(' ');
	}

	function normalizeFeatureCollection(collection, kind) {
		if (!collection || !Array.isArray(collection.features)) return [];
		return collection.features.map((feature, index) => {
			const props = feature.properties || {};
			return {
				id: props.id || props.ObjectId || props.portid || index + 1,
				pageid: props.pageid || '',
				name: props.fullname || props.portname || props.portid || `Unnamed ${kind}`,
				country: props.country || '',
				iso3: props.ISO3 || '',
				observed_on: props.date || props.observed_on || null,
				source_value: props,
				geom: feature.geometry,
				kind,
			};
		});
	}

	function Sparkline({ values, color, height }) {
		const ref = React.useRef(null);
		React.useEffect(() => {
			const canvas = ref.current;
			if (!canvas || !values || values.length < 2) return;
			const ctx = canvas.getContext('2d');
			const width = canvas.clientWidth * 2;
			const h = (height || 100) * 2;
			canvas.width = width;
			canvas.height = h;
			ctx.clearRect(0, 0, width, h);

			const valid = values.filter(v => Number(v) >= 0);
			const max = Math.max(...valid, 1);
			const pad = 8;
			const dw = width - pad * 2;
			const dh = h - pad * 2;
			const pts = values.map((v, i) => ({
				x: pad + (i / (values.length - 1)) * dw,
				y: pad + dh - ((Number(v) || 0) / max) * dh,
			}));

			ctx.beginPath();
			ctx.moveTo(pts[0].x, pts[0].y);
			for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
			ctx.strokeStyle = color || '#63d6ff';
			ctx.lineWidth = 2;
			ctx.lineJoin = 'round';
			ctx.lineCap = 'round';
			ctx.stroke();

			ctx.lineTo(pts[pts.length - 1].x, pad + dh);
			ctx.lineTo(pts[0].x, pad + dh);
			ctx.closePath();
			ctx.fillStyle = `${color || '#63d6ff'}22`;
			ctx.fill();
		}, [values, color, height]);
		return e('canvas', { ref, style: { width: '100%', height: `${height || 100}px` } });
	}

	function App() {
		const [ports, setPorts] = React.useState([]);
		const [chokepoints, setChokepoints] = React.useState([]);
		const [selectedItem, setSelectedItem] = React.useState(null);
		const [mapMode, setMapMode] = React.useState(() => {
			try { return window.localStorage.getItem('portscope-map-mode') || 'globe'; } catch (e) { return 'globe'; }
		});
		const [status, setStatus] = React.useState('Connecting to Portscope...');
		const [monitorQuery, setMonitorQuery] = React.useState('');
		const [monitorPortId, setMonitorPortId] = React.useState('');
		const [monitorActive, setMonitorActive] = React.useState(false);
		const [monitorStatus, setMonitorStatus] = React.useState('Not monitoring');
		const [pwData, setPwData] = React.useState(null);
		const [pwLoading, setPwLoading] = React.useState(false);
		const [pwError, setPwError] = React.useState(null);
		const [aisAnalytics, setAisAnalytics] = React.useState(null);
		const [aisShips, setAisShips] = React.useState([]);
		const [aisLoading, setAisLoading] = React.useState(false);

		const wsRef = React.useRef(null);
		const wsRetryRef = React.useRef(null);
		const analyticsTimerRef = React.useRef(null);
		const mapRef = React.useRef(null);
		const mapContainerRef = React.useRef(null);
		const mapReadyRef = React.useRef(false);

		function stopLiveMonitor() {
			setMonitorActive(false);
			setMonitorStatus('Live monitoring stopped');
			setAisLoading(false);
			if (analyticsTimerRef.current) {
				clearInterval(analyticsTimerRef.current);
				analyticsTimerRef.current = null;
			}
			try {
				if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
					wsRef.current.send(JSON.stringify({ type: 'stop' }));
				}
			} catch (e) {}
		}

		function loadAisAnalytics(portName) {
			if (!portName) return;
			setAisLoading(true);
			fetch(apiUrl('/api/analytics?name=' + encodeURIComponent(portName) + '&mins=10&radius=5000'))
				.then(res => {
					if (!res.ok) throw new Error('Analytics API returned ' + res.status);
					return res.json();
				})
				.then(data => {
					setAisAnalytics(data);
					setAisShips(Array.isArray(data.ships) ? data.ships : []);
					setAisLoading(false);
				})
				.catch(err => {
					console.error('AIS analytics error:', err);
					setAisAnalytics(null);
					setAisShips([]);
					setAisLoading(false);
				});
		}

		function openPortMonitor(port) {
			if (!port) return;
			setSelectedItem({ type: 'port', data: port });
			setMonitorPortId(String(port.id));
			setMonitorActive(true);
			setMonitorStatus(`Monitoring ${port.name}`);
			setStatus(`Monitoring live AIS for ${port.name}`);
			setAisLoading(true);
			try {
				if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
					wsRef.current.send(JSON.stringify({ type: 'monitor', kind: 'port', id: port.id, name: port.name }));
				}
			} catch (e) {}
			loadAisAnalytics(port.name);
			if (analyticsTimerRef.current) clearInterval(analyticsTimerRef.current);
			analyticsTimerRef.current = setInterval(() => loadAisAnalytics(port.name), 5000);
		}

		React.useEffect(() => {
			let alive = true;
			function connect() {
				const ws = new WebSocket(wsUrl('/ws/updates'));
				wsRef.current = ws;
				ws.onopen = () => { if (alive) setStatus('Connected. Loading snapshot...'); };
				ws.onmessage = (event) => {
					try {
						const msg = JSON.parse(event.data);
						if (msg.type === 'snapshot') {
							setPorts(normalizeFeatureCollection({ features: (msg.ports || []).map(item => ({ properties: item.source_value || {}, geometry: item.geom })) }, 'port'));
							setChokepoints(normalizeFeatureCollection({ features: (msg.chokepoints || []).map(item => ({ properties: item.source_value || {}, geometry: item.geom })) }, 'chokepoint'));
							setStatus(`Loaded ${msg.ports?.length || 0} ports and ${msg.chokepoints?.length || 0} chokepoints.`);
							if (!monitorPortId && msg.ports && msg.ports.length > 0) {
								setMonitorPortId(String(msg.ports[0].id));
							}
						}
						if (msg.type === 'selected_record' && msg.kind === 'port' && msg.data) {
							setSelectedItem({ type: 'port', data: msg.data });
						}
					} catch (e) {
						console.warn('ws parse error', e);
					}
				};
				ws.onerror = () => { if (alive) setStatus('Control websocket error; retrying...'); };
				ws.onclose = () => { if (!alive) return; wsRetryRef.current = setTimeout(connect, 2000); };
			}
			connect();
			return () => {
				alive = false;
				if (wsRetryRef.current) clearTimeout(wsRetryRef.current);
				try { if (wsRef.current) wsRef.current.close(); } catch (e) {}
			};
		}, []);

		React.useEffect(() => {
			if (!window.maplibregl || !mapContainerRef.current || mapRef.current) return;
			mapRef.current = new maplibregl.Map({
				container: mapContainerRef.current,
				style: {
					version: 8,
					sources: {
						basemap: {
							type: 'raster',
							tiles: [
								'https://a.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png',
								'https://b.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png',
								'https://c.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png'
							],
							tileSize: 256,
						},
						ports: { type: 'geojson', data: { type: 'FeatureCollection', features: [] } },
						chokepoints: { type: 'geojson', data: { type: 'FeatureCollection', features: [] } },
						ships: { type: 'geojson', data: { type: 'FeatureCollection', features: [] } },
					},
					layers: [{ id: 'basemap', type: 'raster', source: 'basemap' }],
				},
				center: [0, 20],
				zoom: 1.7,
				projection: mapMode === 'globe' ? 'globe' : 'mercator',
				renderWorldCopies: false,
				pitch: mapMode === 'globe' ? 35 : 0,
				bearing: 0,
			});

			mapRef.current.on('load', () => {
				mapReadyRef.current = true;
				try { mapRef.current.setFog({}); } catch (e) {}
				mapRef.current.addLayer({
					id: 'ports-layer',
					type: 'circle',
					source: 'ports',
					paint: {
						'circle-radius': ['interpolate', ['linear'], ['zoom'], 1, 3, 6, 6, 10, 9],
						'circle-color': '#8a7dff',
						'circle-stroke-color': '#08111f',
						'circle-stroke-width': 1,
						'circle-opacity': 0.96,
					},
				});
				mapRef.current.addLayer({
					id: 'ports-labels',
					type: 'symbol',
					source: 'ports',
					layout: {
						'text-field': ['get', 'name'],
						'text-size': 10,
						'text-offset': [0, 1.2],
						'text-anchor': 'top',
					},
					paint: {
						'text-color': '#eef4ff',
						'text-halo-color': '#08111f',
						'text-halo-width': 1.1,
					},
				});
				mapRef.current.addLayer({
					id: 'chokepoints-layer',
					type: 'circle',
					source: 'chokepoints',
					paint: {
						'circle-radius': ['interpolate', ['linear'], ['zoom'], 1, 2.5, 6, 5, 10, 8],
						'circle-color': '#ff8a5b',
						'circle-stroke-color': '#08111f',
						'circle-stroke-width': 1,
						'circle-opacity': 0.96,
					},
				});
				mapRef.current.addLayer({
					id: 'ships-layer',
					type: 'circle',
					source: 'ships',
					paint: {
						'circle-radius': ['interpolate', ['linear'], ['zoom'], 2, 4, 7, 7, 12, 11],
						'circle-color': '#76e4b5',
						'circle-stroke-color': '#08111f',
						'circle-stroke-width': 1,
						'circle-opacity': 0.97,
					},
				});
				mapRef.current.addLayer({
					id: 'ships-labels',
					type: 'symbol',
					source: 'ships',
					layout: {
						'text-field': ['coalesce', ['get', 'ship_name'], ['to-string', ['get', 'mmsi']]],
						'text-size': 10,
						'text-offset': [0, 1.35],
						'text-anchor': 'top',
					},
					paint: {
						'text-color': '#eef4ff',
						'text-halo-color': '#08111f',
						'text-halo-width': 1.2,
					},
				});

				mapRef.current.on('click', 'ports-layer', (event) => {
					const feature = event.features && event.features[0];
					if (!feature) return;
					const record = ports.find(item => String(item.id) === String(feature.properties.id) || String(item.name).toLowerCase() === String(feature.properties.name || '').toLowerCase());
					if (record) {
						setSelectedItem({ type: 'port', data: record });
						setMonitorPortId(String(record.id));
					}
				});
				mapRef.current.on('click', 'chokepoints-layer', (event) => {
					const feature = event.features && event.features[0];
					if (!feature) return;
					const record = chokepoints.find(item => String(item.id) === String(feature.properties.id) || String(item.name).toLowerCase() === String(feature.properties.name || '').toLowerCase());
					if (record) setSelectedItem({ type: 'chokepoint', data: record });
				});
				mapRef.current.on('mouseenter', 'ports-layer', () => { mapRef.current.getCanvas().style.cursor = 'pointer'; });
				mapRef.current.on('mouseleave', 'ports-layer', () => { mapRef.current.getCanvas().style.cursor = ''; });
				mapRef.current.on('mouseenter', 'chokepoints-layer', () => { mapRef.current.getCanvas().style.cursor = 'pointer'; });
				mapRef.current.on('mouseleave', 'chokepoints-layer', () => { mapRef.current.getCanvas().style.cursor = ''; });
			});

			return () => {
				try { mapRef.current.remove(); } catch (e) {}
				mapRef.current = null;
				mapReadyRef.current = false;
			};
		}, []);

		React.useEffect(() => {
			if (!mapRef.current || !mapReadyRef.current) return;
			try {
				if (mapRef.current.getSource('ports')) {
					mapRef.current.getSource('ports').setData({
						type: 'FeatureCollection',
						features: ports.map(port => {
							const coords = pointFromGeometry(port.geom);
							if (!coords) return null;
							return { type: 'Feature', geometry: { type: 'Point', coordinates: coords }, properties: { id: port.id, name: port.name, country: port.country } };
						}).filter(Boolean),
					});
				}
				if (mapRef.current.getSource('chokepoints')) {
					mapRef.current.getSource('chokepoints').setData({
						type: 'FeatureCollection',
						features: chokepoints.map(point => {
							const coords = pointFromGeometry(point.geom);
							if (!coords) return null;
							return { type: 'Feature', geometry: { type: 'Point', coordinates: coords }, properties: { id: point.id, name: point.name } };
						}).filter(Boolean),
					});
				}
				if (mapRef.current.getSource('ships')) {
					mapRef.current.getSource('ships').setData({
						type: 'FeatureCollection',
						features: (aisShips || []).map(ship => ({
							type: 'Feature',
							geometry: { type: 'Point', coordinates: [Number(ship.lon), Number(ship.lat)] },
							properties: { mmsi: ship.mmsi, ship_name: ship.ship_name || '', speed_knots: ship.speed_knots || 0, cog: ship.cog || 0 },
						})),
					});
				}
			} catch (e) {
				console.warn('map source update failed', e);
			}
		}, [ports, chokepoints, aisShips]);

		React.useEffect(() => {
			if (!mapRef.current || !mapReadyRef.current) return;
			if (selectedItem?.type === 'port') {
				try { mapRef.current.setFilter('ports-layer', ['==', ['get', 'id'], Number(selectedItem.data.id)]); } catch (e) {}
			} else {
				try { mapRef.current.setFilter('ports-layer', ['!=', ['get', 'id'], -1]); } catch (e) {}
			}
			if (selectedItem?.type === 'chokepoint') {
				try { mapRef.current.setFilter('chokepoints-layer', ['==', ['get', 'id'], Number(selectedItem.data.id)]); } catch (e) {}
			} else {
				try { mapRef.current.setFilter('chokepoints-layer', ['!=', ['get', 'id'], -1]); } catch (e) {}
			}
		}, [selectedItem]);

		React.useEffect(() => {
			if (!mapRef.current || !mapReadyRef.current) return;
			try {
				mapRef.current.setProjection({ type: mapMode === 'globe' ? 'globe' : 'mercator' });
				mapRef.current.setPitch(mapMode === 'globe' ? 35 : 0);
			} catch (e) {}
			try { window.localStorage.setItem('portscope-map-mode', mapMode); } catch (e) {}
		}, [mapMode]);

		React.useEffect(() => {
			const portName = selectedItem?.type === 'port' ? selectedItem.data?.name : null;
			if (!portName || !monitorActive) return;
			loadAisAnalytics(portName);
			if (analyticsTimerRef.current) clearInterval(analyticsTimerRef.current);
			analyticsTimerRef.current = setInterval(() => loadAisAnalytics(portName), 5000);
			return () => {
				if (analyticsTimerRef.current) clearInterval(analyticsTimerRef.current);
			};
		}, [selectedItem, monitorActive]);

		React.useEffect(() => {
			if (!selectedItem || selectedItem.type !== 'port') return;
			const pageid = selectedItem.data?.pageid;
			if (!pageid) return;
			setPwLoading(true);
			setPwError(null);
			fetch(apiUrl('/api/portwatch/' + pageid + '/data'))
				.then(res => {
					if (!res.ok) throw new Error('API returned ' + res.status);
					return res.json();
				})
				.then(data => {
					setPwData(data);
					setPwLoading(false);
				})
				.catch(err => {
					console.error('PortWatch data error:', err);
					setPwError(err.message);
					setPwLoading(false);
				});
		}, [selectedItem]);

		const monitorMatches = React.useMemo(() => {
			const query = monitorQuery.trim().toLowerCase();
			const items = query
				? ports.filter(port => {
					const name = String(port.name || '').toLowerCase();
					const country = String(port.country || '').toLowerCase();
					return name.includes(query) || country.includes(query) || String(port.id).includes(query);
				})
				: ports;
			return items.slice(0, 20);
		}, [ports, monitorQuery]);

		const monitorSelectedPort = React.useMemo(() => {
			if (!monitorPortId) return null;
			return ports.find(port => String(port.id) === String(monitorPortId)) || null;
		}, [ports, monitorPortId]);

		const selectedData = selectedItem ? selectedItem.data : null;
		const selectedDate = selectedData ? selectedData.observed_on : null;

		const metrics = React.useMemo(() => {
			if (!selectedData) return [];
			const sv = selectedData.source_value || {};
			const items = [];
			if (sv.vessel_count_total !== undefined) items.push({ key: 'Total Vessels', value: formatCount(sv.vessel_count_total) });
			if (sv.vessel_count_container !== undefined) items.push({ key: 'Container Vessels', value: formatCount(sv.vessel_count_container) });
			if (sv.vessel_count_dry_bulk !== undefined) items.push({ key: 'Dry Bulk Vessels', value: formatCount(sv.vessel_count_dry_bulk) });
			if (sv.vessel_count_general_cargo !== undefined) items.push({ key: 'General Cargo Vessels', value: formatCount(sv.vessel_count_general_cargo) });
			if (sv.vessel_count_RoRo !== undefined) items.push({ key: 'RoRo Vessels', value: formatCount(sv.vessel_count_RoRo) });
			if (sv.vessel_count_tanker !== undefined) items.push({ key: 'Tanker Vessels', value: formatCount(sv.vessel_count_tanker) });
			if (sv.vessel_count_total) {
				const total = Number(sv.vessel_count_total);
				if (sv.vessel_count_container !== undefined) items.push({ key: 'Container Share', value: formatPercent(Number(sv.vessel_count_container) / total * 100) });
				if (sv.vessel_count_dry_bulk !== undefined) items.push({ key: 'Dry Bulk Share', value: formatPercent(Number(sv.vessel_count_dry_bulk) / total * 100) });
				if (sv.vessel_count_general_cargo !== undefined) items.push({ key: 'General Cargo Share', value: formatPercent(Number(sv.vessel_count_general_cargo) / total * 100) });
				if (sv.vessel_count_RoRo !== undefined) items.push({ key: 'RoRo Share', value: formatPercent(Number(sv.vessel_count_RoRo) / total * 100) });
				if (sv.vessel_count_tanker !== undefined) items.push({ key: 'Tanker Share', value: formatPercent(Number(sv.vessel_count_tanker) / total * 100) });
			}
			if (sv.industry_top1) items.push({ key: 'Top Industry', value: sv.industry_top1 });
			return items;
		}, [selectedData]);

		function AISSection() {
			const a = aisAnalytics?.analytics || {};
			return e('div', { className: 'ais-section' },
				e('div', { className: 'ais-header' },
					e('span', { className: 'title' }, 'Live AIS Traffic'),
					e('span', { style: { fontSize: '10px', color: 'var(--muted)' } }, a.last_updated ? 'Updated: ' + formatTime(a.last_updated) : (monitorActive ? 'Monitoring...' : 'Idle'))
				),
				monitorActive || aisAnalytics
					? e('div', { className: 'pw-stat-grid' },
						e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, String(a.unique_ships || 0)), e('div', { className: 'lbl' }, 'Ships (10 min)')),
						e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, String(a.underway || 0)), e('div', { className: 'lbl' }, 'Underway')),
						e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, String(a.anchored || 0)), e('div', { className: 'lbl' }, 'Anchored')),
						e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, (a.avg_speed_knots || 0).toFixed(1)), e('div', { className: 'lbl' }, 'Avg kts'))
					)
					: e('div', { className: 'pw-empty' }, 'Pick a port and start live monitoring to stream AIS ships here.'),
				Array.isArray(aisShips) && aisShips.length > 0
					? e('div', { className: 'ais-ships' },
						e('div', { style: { fontSize: '10px', textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--muted)', marginBottom: '4px', marginTop: '8px' } }, 'Recent ships'),
						aisShips.slice(0, 12).map(ship => e('div', { className: 'ais-ship-row', key: ship.mmsi },
							e('span', { className: 'ais-mmsi' }, String(ship.mmsi)),
							e('span', { className: 'ais-name', title: ship.ship_name || '' }, ship.ship_name && ship.ship_name.length > 14 ? ship.ship_name.slice(0, 14) + '…' : (ship.ship_name || '—')),
							e('span', { className: 'ais-speed' }, (ship.speed_knots || 0).toFixed(1) + ' kts'),
							e('span', { className: 'ais-cog' }, (ship.cog || 0).toFixed(0) + '°')
						))
					)
					: null
			);
		}

		return e('div', { className: 'shell' },
			e('div', { className: 'topbar' },
				e('div', { className: 'brand' },
					e('h1', null, 'Portscope'),
					e('p', null, 'Live port analytics, AIS monitoring, and chokepoint intelligence')
				),
				e('div', { className: 'status-pill' }, e('span', { className: 'status-dot' }), e('span', null, status))
			),
			e('div', { className: 'workspace' },
				e('div', { className: 'sidebar' },
					e('div', { className: 'panel section-surface' },
						e('div', { className: 'panel-body' },
							e('div', { className: 'panel-title' }, 'Selected record'),
							e('div', { className: 'summary-name' }, selectedData ? selectedData.name : 'Choose a record'),
							e('div', { className: 'chip', style: { marginTop: '10px' } }, selectedDate ? formatTime(selectedDate) : 'Waiting for selection')
						)
					),
					e('section', { className: 'panel section-surface' },
						e('div', { className: 'panel-header' }, e('div', { className: 'panel-title' }, 'Live AIS Monitor'), e('span', { className: 'chip' }, monitorActive ? 'Running' : 'Idle')),
						e('div', { className: 'panel-body', style: { display: 'grid', gap: '10px' } },
							e('div', { className: 'empty-state', style: { padding: '10px 12px' } }, monitorStatus),
							e('input', { value: monitorQuery, onChange: ev => setMonitorQuery(ev.target.value), placeholder: 'Search ports by name or country', style: { width: '100%', padding: '10px 12px', borderRadius: '12px', border: '1px solid var(--border)', background: 'rgba(255,255,255,0.04)', color: 'var(--text)' } }),
							e('select', { value: monitorPortId, onChange: ev => setMonitorPortId(ev.target.value), size: 8, style: { width: '100%', padding: '10px', borderRadius: '14px', border: '1px solid var(--border)', background: 'rgba(255,255,255,0.04)', color: 'var(--text)' } },
								monitorMatches.map(port => e('option', { key: port.id, value: String(port.id) }, formatPortLabel(port)))
							),
							e('div', { style: { display: 'flex', gap: '8px', flexWrap: 'wrap' } },
								e('button', { type: 'button', onClick: () => openPortMonitor(monitorSelectedPort || (selectedItem && selectedItem.type === 'port' ? selectedItem.data : null)), disabled: !(monitorSelectedPort || (selectedItem && selectedItem.type === 'port')), style: { padding: '8px 12px', borderRadius: '999px', border: '1px solid var(--border)', background: 'rgba(118, 228, 181, 0.14)', color: 'var(--text)', cursor: 'pointer' } }, 'Start live'),
								e('button', { type: 'button', onClick: stopLiveMonitor, disabled: !monitorActive, style: { padding: '8px 12px', borderRadius: '999px', border: '1px solid var(--border)', background: 'rgba(255,255,255,0.04)', color: 'var(--text)', cursor: 'pointer' } }, 'Stop')
							),
							e('div', { className: 'hint' }, 'Choose a port, then start live monitoring to stream AIS updates until you stop it.')
						)
					),
					e('section', { className: 'panel section-surface' },
						e('div', { className: 'panel-header' }, e('div', { className: 'panel-title' }, 'Port Metrics')),
						e('div', { className: 'panel-body' }, metrics.length > 0 ? e('div', { className: 'metric-list' }, metrics.map(item => e('div', { key: item.key, className: 'metric-item' }, e('span', null, item.key), e('small', null, item.value)))) : e('div', { className: 'empty-state' }, 'No record selected.'))
					)
				),
				e('div', { className: `map-shell ${mapMode === 'globe' ? 'mode-globe' : 'mode-2d'} ${mapReadyRef.current ? 'is-ready' : 'is-loading'}` },
					e('div', { className: 'map-header' },
						e('div', { className: 'map-card' },
							e('span', { className: 'eyebrow' }, 'Map view'),
							e('h3', null, mapMode === 'globe' ? '3D globe mode' : 'Fixed 2D map'),
							e('p', null, mapMode === 'globe' ? 'Globe mode is optimized for spatial context.' : 'Interactive map with live AIS overlays.'),
							e('div', { style: { display: 'flex', gap: '8px', marginTop: '10px', flexWrap: 'wrap' } },
								e('button', { type: 'button', onClick: () => setMapMode('2d'), style: { padding: '7px 10px', borderRadius: '999px', border: '1px solid var(--border)', background: mapMode === '2d' ? 'rgba(99, 214, 255, 0.18)' : 'rgba(255,255,255,0.04)', color: 'var(--text)', cursor: 'pointer' } }, 'Fixed 2D'),
								e('button', { type: 'button', onClick: () => setMapMode('globe'), style: { padding: '7px 10px', borderRadius: '999px', border: '1px solid var(--border)', background: mapMode === 'globe' ? 'rgba(99, 214, 255, 0.18)' : 'rgba(255,255,255,0.04)', color: 'var(--text)', cursor: 'pointer' } }, '3D Globe')
							),
							e('div', { className: 'map-legend' },
								e('div', { className: 'legend-title' }, 'Legend'),
								e('div', { className: 'legend-row' }, e('span', { className: 'legend-swatch legend-port' }), e('span', null, 'Port')),
								e('div', { className: 'legend-row' }, e('span', { className: 'legend-swatch legend-chokepoint' }), e('span', null, 'Chokepoint')),
								e('div', { className: 'legend-row' }, e('span', { className: 'legend-swatch legend-selected' }), e('span', null, 'Selected')),
								e('div', { className: 'legend-row' }, e('span', { className: 'legend-swatch', style: { background: '#76e4b5' } }), e('span', null, 'Live ship'))
							)
						)
					),
					e('div', { ref: mapContainerRef, id: 'map' })
				),
				e('div', { className: 'sidebar right-rail', style: { padding: '12px', gap: '10px' } },
					e('div', { className: 'pw-header' },
						e('span', { className: 'title' }, selectedItem ? selectedItem.data.name : 'PortWatch'),
						selectedItem?.data?.pageid ? e('a', { className: 'ext-link', href: apiUrl('/portwatch/' + selectedItem.data.pageid), target: '_blank' }, 'Open on PortWatch →') : null
					),
					!selectedItem
						? e('div', { className: 'pw-empty' }, 'Click a port or chokepoint to view analytics.')
						: pwLoading
						? e('div', { className: 'pw-loading' }, 'Loading PortWatch data...')
						: pwError
						? e('div', { className: 'pw-error' }, 'Could not load PortWatch data. Visit the external link above.')
						: pwData && pwData.metrics
						? e(React.Fragment, null,
							e(AISSection),
							e('div', { className: 'pw-stat-grid' },
								e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, formatCount(pwData.metrics.total_portcalls)), e('div', { className: 'lbl' }, 'Port Calls')),
								e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, formatCount(Math.round(pwData.metrics.avg_daily_portcalls))), e('div', { className: 'lbl' }, 'Avg Daily')),
								e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, formatCount(Math.round(pwData.metrics.total_imports / 1e6)) + 'M'), e('div', { className: 'lbl' }, 'Imports (tons)')),
								e('div', { className: 'pw-stat' }, e('div', { className: 'val' }, formatCount(Math.round(pwData.metrics.total_exports / 1e6)) + 'M'), e('div', { className: 'lbl' }, 'Exports (tons)'))
							),
							e('div', { style: { fontSize: '10px', color: 'var(--muted)', textAlign: 'center', padding: '2px 0' } }, 'Data: ' + (pwData.metrics.data_range_start || '?') + ' → ' + (pwData.metrics.data_range_end || '?')),
							e('div', { className: 'pw-chart-box' }, e('div', { className: 'pw-chart-label' }, 'Daily Port Calls (last 30)'), e(Sparkline, { values: pwData.timeseries && pwData.timeseries.portcalls ? pwData.timeseries.portcalls.slice(-30).map(p => p.value) : [], color: '#63d6ff', height: 100 })),
							pwData.unavailable_data && pwData.unavailable_data.length > 0 ? e('div', { style: { display: 'flex', flexDirection: 'column', gap: '4px', borderTop: '1px solid var(--border)', paddingTop: '10px', marginTop: '6px' } },
								e('div', { style: { fontSize: '10px', textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--muted)', marginBottom: '4px' } }, 'Additional data on PortWatch'),
								pwData.unavailable_data.map((item, i) => e('a', { key: i, className: 'pw-ext-link', href: item.external_url || pwData.external_url, target: '_blank' }, e('span', { className: 'icn', style: { color: 'var(--accent)', fontSize: '12px' } }, '↗'), e('div', null, e('div', null, item.label), e('div', { className: 'desc' }, item.description ? item.description.slice(0, 80) + '...' : ''))))
							) : null
						)
						: e(React.Fragment, null, e(AISSection), e('div', { className: 'pw-empty' }, 'No PortWatch data available for this item.'))
				)
			)
		);
	}

	ReactDOM.createRoot(document.getElementById('app')).render(e(App));
})();
