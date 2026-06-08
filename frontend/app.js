(function(){
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

function pointFromGeometry(geom) {
	if (!geom) return null;
	const parsed = typeof geom === 'string' ? JSON.parse(geom) : geom;
	if (parsed && parsed.type === 'Point' && Array.isArray(parsed.coordinates) && parsed.coordinates.length >= 2) {
		const [lon, lat] = parsed.coordinates;
		if (Number.isFinite(lon) && Number.isFinite(lat) && lon >= -180 && lon <= 180 && lat >= -90 && lat <= 90) {
			return parsed.coordinates;
		}
	}
	return null;
}

function formatDate(value) {
	if (!value) return 'Unknown';
	return new Date(value).toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
}

function formatCount(value) {
	if (value === null || value === undefined || value === '') return 'n/a';
	const number = Number(value);
	return Number.isFinite(number) ? number.toLocaleString() : String(value);
}

function formatPercent(value) {
	if (value === null || value === undefined || value === '') return 'n/a';
	const number = Number(value);
	return Number.isFinite(number) ? `${number.toFixed(1)}%` : String(value);
}

function normalizeFeatureCollection(collection, kind) {
	if (!collection || !Array.isArray(collection.features)) return [];
	return collection.features.map((feature, index) => {
		const properties = feature.properties || {};
		return {
			id: properties.id || properties.ObjectId || properties.portid || index + 1,
			pageid: properties.pageid || '',
			name: properties.fullname || properties.portname || properties.portid || `Unnamed ${kind}`,
			country: properties.country || '',
			iso3: properties.ISO3 || '',
			observed_on: properties.date || properties.observed_on || null,
			source_value: properties,
			geom: feature.geometry,
			kind,
		};
	});
}

// Sparkline draws itself using a ref callback so canvas renders correctly
function Sparkline({ values, color, height }) {
	const drawTimeout = React.useRef(null);
	const drawChart = React.useCallback((canvas) => {
		if (!canvas || !values || values.length < 2) return;
		// Clear any pending draw
		if (drawTimeout.current) clearTimeout(drawTimeout.current);
		drawTimeout.current = setTimeout(() => {
			const ctx = canvas.getContext('2d');
			const w = canvas.width = canvas.clientWidth * 2;
			const h = canvas.height = (height || 100) * 2;
			ctx.clearRect(0, 0, w, h);

			const valid = values.filter(v => v > 0);
			const max = Math.max(...valid, 1);
			const min = 0;
			const range = max - min || 1;
			const pad = 6;
			const dw = w - pad * 2;
			const dh = h - pad * 2;
			const len = values.length;
			if (len < 2) return;

			const pts = values.map((v, i) => ({
				x: pad + (i / (len - 1)) * dw,
				y: pad + dh - ((v - min) / range) * dh
			}));

			// Fill
			ctx.beginPath();
			const grad = ctx.createLinearGradient(0, pad, 0, pad + dh);
			grad.addColorStop(0, (color || '#63d6ff') + '25');
			grad.addColorStop(1, (color || '#63d6ff') + '02');
			ctx.fillStyle = grad;
			ctx.moveTo(pts[0].x, pts[0].y);
			for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
			ctx.lineTo(pts[pts.length - 1].x, pad + dh);
			ctx.lineTo(pts[0].x, pad + dh);
			ctx.closePath();
			ctx.fill();

			// Line
			ctx.beginPath();
			ctx.strokeStyle = color || '#63d6ff';
			ctx.lineWidth = 2;
			ctx.lineJoin = 'round';
			ctx.lineCap = 'round';
			ctx.moveTo(pts[0].x, pts[0].y);
			for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
			ctx.stroke();
		}, 30);
	}, [values, color, height]);
	return e('canvas', { ref: drawChart, style: { width: '100%', height: (height || 100) + 'px' } });
}

function App() {
	const [ports, setPorts] = React.useState([]);
	const [chokepoints, setChokepoints] = React.useState([]);
	const [selectedItem, setSelectedItem] = React.useState(null);
	const [mapMode, setMapMode] = React.useState(() => {
		try {
			return window.localStorage.getItem('portscope-map-mode') || '2d';
		} catch (e) {
			return '2d';
		}
	});
	const [status, setStatus] = React.useState('Loading PortWatch datasets...');
	const [pwData, setPwData] = React.useState(null);
	const [pwLoading, setPwLoading] = React.useState(false);
	const [pwError, setPwError] = React.useState(null);
	const [aisAnalytics, setAisAnalytics] = React.useState(null);
	const [aisLoading, setAisLoading] = React.useState(false);
	const aisPrevName = React.useRef(null);

	function resetAIS() {
		setAisAnalytics(null);
		setAisLoading(false);
		aisPrevName.current = null;
	}

	const mapRef = React.useRef(null);
	const portLayerRef = React.useRef(null);
	const chokepointLayerRef = React.useRef(null);
	const canvasRendererRef = React.useRef(null);
	const portMarkersRef = React.useRef(new Map());
	const chokepointMarkersRef = React.useRef(new Map());
	const renderTimeoutRef = React.useRef(null);
	const viewStateRef = React.useRef({ center: [20, 20], zoom: 2, bearing: 0, pitch: 0 });
	const pwPrevPageid = React.useRef(null);
	const dataLoadedRef = React.useRef(false);
	const [mapReady, setMapReady] = React.useState(false);

	function selectItem(type, data) {
		setSelectedItem({ type, data });
		setStatus(`${data.name} selected.`);
	}

	function setMode(nextMode) {
		try {
			if (mapRef.current && typeof mapRef.current.getCenter === 'function') {
				const center = mapRef.current.getCenter();
				viewStateRef.current = {
					center: [center.lng, center.lat],
					zoom: typeof mapRef.current.getZoom === 'function' ? mapRef.current.getZoom() : viewStateRef.current.zoom,
					bearing: typeof mapRef.current.getBearing === 'function' ? mapRef.current.getBearing() : viewStateRef.current.bearing,
					pitch: typeof mapRef.current.getPitch === 'function' ? mapRef.current.getPitch() : viewStateRef.current.pitch,
				};
			}
		} catch (e) {}
		setMapReady(false);
		setMapMode(nextMode);
		try {
			window.localStorage.setItem('portscope-map-mode', nextMode);
		} catch (e) {}
		setStatus(nextMode === 'globe' ? 'Globe mode enabled.' : 'Fixed 2D mode enabled.');
	}

	function findRecordByFeature(feature, records) {
		if (!feature || !feature.properties) return null;
		const featureId = feature.properties.id || feature.properties.ID || feature.properties.objectid || feature.properties.ObjectId || feature.properties.portid;
		const featureName = feature.properties.name || feature.properties.fullname || feature.properties.portname || feature.properties.portid;
		const featurePageId = feature.properties.pageid || feature.properties.PageId || feature.properties.PAGEID;
		if (featureId !== undefined && featureId !== null) {
			const numericId = Number(featureId);
			const byId = records.find(record => Number(record.id) === numericId || String(record.id) === String(featureId));
			if (byId) return byId;
		}
		if (featurePageId) {
			const loweredPageId = String(featurePageId).toLowerCase();
			const byPageId = records.find(record => String(record.pageid || '').toLowerCase() === loweredPageId);
			if (byPageId) return byPageId;
		}
		if (featureName) {
			const lowered = String(featureName).toLowerCase();
			const byName = records.find(record => String(record.name || '').toLowerCase() === lowered);
			if (byName) return byName;
		}
		return null;
	}

	// Fetch PortWatch data (by pageid)
	React.useEffect(() => {
		const pageid = selectedItem?.data?.pageid;
		if (!pageid || selectedItem?.type !== 'port') {
			setPwData(null);
			setPwLoading(false);
			setPwError(null);
			pwPrevPageid.current = null;
			return;
		}
		if (pageid === pwPrevPageid.current) return;
		pwPrevPageid.current = pageid;

		if (pageid) {
			setPwLoading(true);
			setPwError(null);
			setPwData(null);

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
		} else {
			setPwLoading(false);
			setPwError('No PortWatch ID available');
		}
	}, [selectedItem]);

	// Fetch AIS analytics (by port name from GeoJSON fullname)

	React.useEffect(() => {
		const portName = selectedItem?.data?.name;
		if (!portName || selectedItem?.type !== 'port') {
			resetAIS();
			return;
		}
		if (portName === aisPrevName.current) return;
		aisPrevName.current = portName;

		setAisLoading(true);
		setAisAnalytics(null);

		fetch(apiUrl('/api/analytics?name=' + encodeURIComponent(portName) + '&mins=10&radius=5000'))
			.then(res => {
				if (!res.ok) throw new Error('Analytics API returned ' + res.status);
				return res.json();
			})
			.then(data => {
				setAisAnalytics(data);
				setAisLoading(false);
			})
			.catch(err => {
				console.error('AIS analytics error:', err);
				setAisAnalytics(null);
				setAisLoading(false);
			});
	}, [selectedItem]);

	function renderLayers() {
		// Debounce rendering to avoid jank during rapid updates
		if (renderTimeoutRef.current) clearTimeout(renderTimeoutRef.current);
		renderTimeoutRef.current = setTimeout(() => {
			if (!portLayerRef.current || !chokepointLayerRef.current) return;
			const hasSelection = Boolean(selectedItem);

			// Ports: remove stale, update existing, add new
			const newPortIds = new Set(ports.map(p => p.id));
			const existingPorts = portMarkersRef.current;
			existingPorts.forEach((marker, id) => {
				if (!newPortIds.has(id)) {
					portLayerRef.current.removeLayer(marker);
					existingPorts.delete(id);
				}
			});
			ports.forEach(item => {
				const coords = pointFromGeometry(item.geom);
				if (!coords) return;
				const [lon, lat] = coords;
				const active = hasSelection && selectedItem.type === 'port' && selectedItem.data.id === item.id;
				const style = { radius: active ? 10 : 7, color: active ? '#63d6ff' : '#8a7dff', weight: 2, fillColor: active ? '#63d6ff' : '#c9c1ff', fillOpacity: 0.92 };
				if (existingPorts.has(item.id)) {
					const m = existingPorts.get(item.id);
					m.setLatLng([lat, lon]);
					if (m.setStyle) m.setStyle(style);
				} else {
					const marker = L.circleMarker([lat, lon], Object.assign({}, style, { renderer: canvasRendererRef.current }));
					marker.bindTooltip(`<b>${item.name}</b><br/>Port intelligence`, { direction: 'top', offset: [0, -8] });
					marker.on('click', () => selectItem('port', item));
					portLayerRef.current.addLayer(marker);
					existingPorts.set(item.id, marker);
				}
			});

			// Chokepoints: same workflow
			const newChokeIds = new Set(chokepoints.map(p => p.id));
			const existingChokes = chokepointMarkersRef.current;
			existingChokes.forEach((marker, id) => {
				if (!newChokeIds.has(id)) {
					chokepointLayerRef.current.removeLayer(marker);
					existingChokes.delete(id);
				}
			});
			chokepoints.forEach(item => {
				const coords = pointFromGeometry(item.geom);
				if (!coords) return;
				const [lon, lat] = coords;
				const active = hasSelection && selectedItem.type === 'chokepoint' && selectedItem.data.id === item.id;
				const style = { radius: active ? 9 : 6, color: active ? '#ffcc66' : '#ff8a5b', weight: 2, fillColor: active ? '#ffcc66' : '#ffb38f', fillOpacity: 0.95 };
				if (existingChokes.has(item.id)) {
					const m = existingChokes.get(item.id);
					m.setLatLng([lat, lon]);
					if (m.setStyle) m.setStyle(style);
				} else {
					const marker = L.circleMarker([lat, lon], Object.assign({}, style, { renderer: canvasRendererRef.current }));
					marker.bindTooltip(`<b>${item.name}</b><br/>Chokepoint intensity`, { direction: 'top', offset: [0, -8] });
					marker.on('click', () => selectItem('chokepoint', item));
					chokepointLayerRef.current.addLayer(marker);
					existingChokes.set(item.id, marker);
				}
			});
		}, 40);
	}

	function clearMapArtifacts() {
		if (renderTimeoutRef.current) {
			clearTimeout(renderTimeoutRef.current);
			renderTimeoutRef.current = null;
		}
		if (mapRef.current) {
			try { mapRef.current.remove(); } catch (e) {}
			mapRef.current = null;
		}
		portLayerRef.current = null;
		chokepointLayerRef.current = null;
		canvasRendererRef.current = null;
		portMarkersRef.current = new Map();
		chokepointMarkersRef.current = new Map();
		dataLoadedRef.current = false;
		setMapReady(false);
	}

	async function loadData() {
		setStatus('Loading local PortWatch datasets...');
		try {
			const [portsResponse, chokepointsResponse] = await Promise.all([
				fetch('/data/Ports.geojson'),
				fetch('/data/Chokepoints.geojson'),
			]);
			const [portsData, chokepointsData] = await Promise.all([
				portsResponse.json(),
				chokepointsResponse.json(),
			]);

			const normalizedPorts = normalizeFeatureCollection(portsData, 'port');
			const normalizedChokepoints = normalizeFeatureCollection(chokepointsData, 'chokepoint');

			setPorts(normalizedPorts);
			setChokepoints(normalizedChokepoints);

			if (normalizedPorts.length > 0 || normalizedChokepoints.length > 0) {
				setStatus(`Loaded ${normalizedPorts.length} ports and ${normalizedChokepoints.length} chokepoints.`);
			} else {
				setStatus('No local PortWatch records returned yet.');
			}
		} catch (err) {
			console.error(err);
			setStatus('Unable to load local PortWatch data.');
		}
	}

	React.useEffect(() => {
		clearMapArtifacts();
		setTimeout(() => {
			const mapEl = document.getElementById('map');
			if (!mapEl) return;

			// Fixed 2D mode uses Leaflet; globe mode uses MapLibre.
			if (mapMode === 'globe' && typeof maplibregl !== 'undefined') {
				mapRef.current = new maplibregl.Map({
					container: 'map',
					style: {
						version: 8,
						sources: {},
						layers: [
							{ id: 'background', type: 'background', paint: { 'background-color': '#061018' } }
						]
					},
					center: viewStateRef.current.center,
					zoom: viewStateRef.current.zoom,
					renderWorldCopies: false,
					projection: 'globe',
					dragRotate: true,
					pitchWithRotate: true,
					keyboard: true,
				});

				try { mapRef.current.setPitch(Math.max(35, viewStateRef.current.pitch || 35)); } catch (e) {}
				try { mapRef.current.setBearing(viewStateRef.current.bearing || 0); } catch (e) {}
				try { mapRef.current.setFog({}); } catch (e) {}

				mapRef.current.on('load', () => {
					// Add a raster basemap as visual context (Carto dark tiles)
					try {
						mapRef.current.addSource('basemap', {
							type: 'raster',
							tiles: [
								'https://a.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png',
								'https://b.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png',
								'https://c.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png'
							],
							tileSize: 256,
						});
						mapRef.current.addLayer({ id: 'basemap-layer', type: 'raster', source: 'basemap' });
					} catch (e) {
						// ignore if basemap fails to add
					}

					// Add vector tile sources
					mapRef.current.addSource('ports', {
						type: 'vector',
						tiles: [apiUrl('/tiles/ports/{z}/{x}/{y}.pbf')],
						maxzoom: 14,
					});
					mapRef.current.addLayer({
						id: 'ports-layer',
						type: 'circle',
						source: 'ports',
						'source-layer': 'ports',
						paint: {
							'circle-radius': 4,
							'circle-color': '#8a7dff',
							'circle-stroke-color': '#08111f',
							'circle-stroke-width': 1,
							'circle-opacity': 0.95,
						},
					});

					mapRef.current.addSource('chokepoints', {
						type: 'vector',
						tiles: [apiUrl('/tiles/chokepoints/{z}/{x}/{y}.pbf')],
						maxzoom: 14,
					});
					mapRef.current.addLayer({
						id: 'chokepoints-layer',
						type: 'circle',
						source: 'chokepoints',
						'source-layer': 'chokepoints',
						paint: {
							'circle-radius': 3.5,
							'circle-color': '#ff8a5b',
							'circle-stroke-color': '#08111f',
							'circle-stroke-width': 1,
							'circle-opacity': 0.95,
						},
					});

					// Highlight layers (empty filter initially)
					mapRef.current.addLayer({
						id: 'ports-highlight',
						type: 'circle',
						source: 'ports',
						'source-layer': 'ports',
						paint: { 'circle-radius': 8, 'circle-color': '#63d6ff', 'circle-opacity': 0.95 },
						filter: ['==', 'id', ''],
					});
					mapRef.current.addLayer({
						id: 'chokepoints-highlight',
						type: 'circle',
						source: 'chokepoints',
						'source-layer': 'chokepoints',
						paint: { 'circle-radius': 8, 'circle-color': '#ffcc66', 'circle-opacity': 0.95 },
						filter: ['==', 'id', ''],
					});

					// Click handlers
					mapRef.current.on('click', 'ports-layer', (e) => {
						const feat = e.features && e.features[0];
						const record = findRecordByFeature(feat, ports);
						if (record) selectItem('port', record);
					});
						mapRef.current.on('mouseenter', 'ports-layer', () => {
							mapRef.current.getCanvas().style.cursor = 'pointer';
						});
						mapRef.current.on('mouseleave', 'ports-layer', () => {
							mapRef.current.getCanvas().style.cursor = '';
						});
					mapRef.current.on('click', 'chokepoints-layer', (e) => {
						const feat = e.features && e.features[0];
						const record = findRecordByFeature(feat, chokepoints);
						if (record) selectItem('chokepoint', record);
					});
						mapRef.current.on('mouseenter', 'chokepoints-layer', () => {
							mapRef.current.getCanvas().style.cursor = 'pointer';
						});
						mapRef.current.on('mouseleave', 'chokepoints-layer', () => {
							mapRef.current.getCanvas().style.cursor = '';
						});

						mapRef.current.on('moveend', () => {
							try {
								const center = mapRef.current.getCenter();
								viewStateRef.current = {
									center: [center.lng, center.lat],
									zoom: mapRef.current.getZoom(),
									bearing: mapRef.current.getBearing(),
									pitch: mapRef.current.getPitch(),
								};
							} catch (e) {}
						});

						// Fallback click path if the layer event misses in some browsers
						mapRef.current.on('click', (e) => {
							const hits = mapRef.current.queryRenderedFeatures(e.point, {
								layers: ['ports-layer', 'chokepoints-layer']
							});
							if (!hits || hits.length === 0) return;
							const hit = hits[0];
							if (hit.layer && hit.layer.id === 'ports-layer') {
								const record = findRecordByFeature(hit, ports);
								if (record) selectItem('port', record);
							} else if (hit.layer && hit.layer.id === 'chokepoints-layer') {
								const record = findRecordByFeature(hit, chokepoints);
								if (record) selectItem('chokepoint', record);
							}
						});

						setMapReady(true);
				});

				loadData();
				return;
			}

			// Leaflet default: fixed-view 2D map with clustering and canvas markers.
			if (typeof L === 'undefined') {
				setStatus('Map libraries not loaded; map unavailable.');
				return;
			}
			const mapEl2 = document.getElementById('map');
			if (!mapEl2 || mapEl2._leaflet_id) return;
			mapRef.current = L.map('map', {
				zoomControl: true,
				worldCopyJump: true,
				dragging: true,
				scrollWheelZoom: true,
				doubleClickZoom: true,
				boxZoom: true,
				keyboard: true,
				maxBounds: [[-180, -80], [180, 85]],
				maxBoundsViscosity: 1.0,
			}).setView([viewStateRef.current.center[1], viewStateRef.current.center[0]], viewStateRef.current.zoom);
			L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', {
				maxZoom: 19,
				attribution: '&copy; OpenStreetMap contributors &copy; CARTO',
			}).addTo(mapRef.current);
			mapRef.current.on('moveend zoomend', () => {
				const center = mapRef.current.getCenter();
				viewStateRef.current = {
					center: [center.lng, center.lat],
					zoom: mapRef.current.getZoom(),
					bearing: 0,
					pitch: 0,
				};
			});
			// Canvas renderer for faster vector draws
			canvasRendererRef.current = L.canvas({ padding: 0.5 });
			// Marker clustering (uses leaflet.markercluster included in index.html)
			portLayerRef.current = L.markerClusterGroup({
				chunkedLoading: true,
				spiderfyOnMaxZoom: false,
				showCoverageOnHover: false,
				maxClusterRadius: 48,
				disableClusteringAtZoom: 10,
			}).addTo(mapRef.current);
			chokepointLayerRef.current = L.markerClusterGroup({
				chunkedLoading: true,
				spiderfyOnMaxZoom: false,
				showCoverageOnHover: false,
				maxClusterRadius: 40,
				disableClusteringAtZoom: 10,
			}).addTo(mapRef.current);
			setMapReady(true);
			loadData();
		}, 50);
		return () => {
			clearMapArtifacts();
		};
	}, [mapMode]);

	React.useEffect(() => {
		if (!mapRef.current) return;
		if ((ports.length === 0 && chokepoints.length === 0)) return;
		renderLayers();
		if (!dataLoadedRef.current) {
			dataLoadedRef.current = true;
			setTimeout(() => {
				if (!mapRef.current) return;
				// Resize for MapLibre or Leaflet
				if (mapRef.current.resize) {
					try { mapRef.current.resize(); } catch (e) {}
				} else if (mapRef.current.invalidateSize) {
					try { mapRef.current.invalidateSize(); } catch (e) {}
				}
				const allCoords = [];
				ports.forEach(p => { const c = pointFromGeometry(p.geom); if (c) allCoords.push([c[1], c[0]]); });
				chokepoints.forEach(p => { const c = pointFromGeometry(p.geom); if (c) allCoords.push([c[1], c[0]]); });
				if (allCoords.length === 0) return;
				if (typeof maplibregl !== 'undefined' && mapRef.current && mapRef.current.fitBounds) {
					// MapLibre expects [[minLon,minLat],[maxLon,maxLat]]
					const lonlats = allCoords.map(([lat, lon]) => [lon, lat]);
					if (lonlats.length > 0) {
						let minLon = lonlats[0][0], minLat = lonlats[0][1], maxLon = lonlats[0][0], maxLat = lonlats[0][1];
						lonlats.forEach(([lon, lat]) => {
							if (lon < minLon) minLon = lon;
							if (lon > maxLon) maxLon = lon;
							if (lat < minLat) minLat = lat;
							if (lat > maxLat) maxLat = lat;
						});
						mapRef.current.fitBounds([[minLon, minLat], [maxLon, maxLat]], { padding: 30, maxZoom: 4 });
					}
				} else {
					try { mapRef.current.fitBounds(allCoords, { padding: [30, 30], maxZoom: 4 }); } catch (e) {}
				}
			}, 200);
		}
	}, [ports, chokepoints]);

	// When selection changes, update MapLibre highlight filters (if used)
	React.useEffect(() => {
		if (!mapRef.current || typeof maplibregl === 'undefined') return;
		if (!selectedItem) {
			try { mapRef.current.setFilter('ports-highlight', ['==', 'id', '']); } catch (e) {}
			try { mapRef.current.setFilter('chokepoints-highlight', ['==', 'id', '']); } catch (e) {}
			return;
		}
		if (selectedItem.type === 'port') {
			try { mapRef.current.setFilter('ports-highlight', ['==', 'id', Number(selectedItem.data.id)]); } catch (e) {}
			try { mapRef.current.setFilter('chokepoints-highlight', ['==', 'id', '']); } catch (e) {}
		} else if (selectedItem.type === 'chokepoint') {
			try { mapRef.current.setFilter('chokepoints-highlight', ['==', 'id', Number(selectedItem.data.id)]); } catch (e) {}
			try { mapRef.current.setFilter('ports-highlight', ['==', 'id', '']); } catch (e) {}
		}
	}, [selectedItem]);

	React.useEffect(() => {
		if (!mapRef.current) return;
		if (mapMode !== 'globe') return;
		if (typeof maplibregl === 'undefined') return;
		if (!mapRef.current.setProjection) return;
		try { mapRef.current.setProjection({ type: 'globe' }); } catch (e) {}
	}, [mapMode]);

	const mapLegend = e('div', { className: 'map-legend' },
		e('div', { className: 'legend-title' }, 'Legend'),
		e('div', { className: 'legend-row' }, e('span', { className: 'legend-swatch legend-port' }), e('span', null, 'Port')),
		e('div', { className: 'legend-row' }, e('span', { className: 'legend-swatch legend-chokepoint' }), e('span', null, 'Chokepoint')),
		e('div', { className: 'legend-row' }, e('span', { className: 'legend-swatch legend-selected' }), e('span', null, 'Selected')),
		e('div', { className: 'legend-note' }, mapMode === 'globe' ? 'Globe mode is optional.' : 'Fixed 2D mode is the default.')
	);

	const selectedData = selectedItem ? selectedItem.data : null;
	const selectedDate = selectedData ? selectedData.observed_on : null;

	const metrics = (() => {
		if (!selectedData) return [];
		const sv = selectedData.source_value || selectedData.metrics || {};
		const totalVessels = Number(sv.vessel_count_total);
		const items = [];
		if (sv.vessel_count_total !== undefined) items.push({ key: 'Total Vessels', value: formatCount(sv.vessel_count_total) });
		if (sv.vessel_count_container !== undefined) items.push({ key: 'Container Vessels', value: formatCount(sv.vessel_count_container) });
		if (sv.vessel_count_dry_bulk !== undefined) items.push({ key: 'Dry Bulk Vessels', value: formatCount(sv.vessel_count_dry_bulk) });
		if (sv.vessel_count_general_cargo !== undefined) items.push({ key: 'General Cargo Vessels', value: formatCount(sv.vessel_count_general_cargo) });
		if (sv.vessel_count_RoRo !== undefined) items.push({ key: 'RoRo Vessels', value: formatCount(sv.vessel_count_RoRo) });
		if (sv.vessel_count_tanker !== undefined) items.push({ key: 'Tanker Vessels', value: formatCount(sv.vessel_count_tanker) });
		if (Number.isFinite(totalVessels) && totalVessels > 0) {
			if (sv.vessel_count_container !== undefined) items.push({ key: 'Container Share', value: formatPercent((Number(sv.vessel_count_container) / totalVessels) * 100) });
			if (sv.vessel_count_dry_bulk !== undefined) items.push({ key: 'Dry Bulk Share', value: formatPercent((Number(sv.vessel_count_dry_bulk) / totalVessels) * 100) });
			if (sv.vessel_count_general_cargo !== undefined) items.push({ key: 'General Cargo Share', value: formatPercent((Number(sv.vessel_count_general_cargo) / totalVessels) * 100) });
			if (sv.vessel_count_RoRo !== undefined) items.push({ key: 'RoRo Share', value: formatPercent((Number(sv.vessel_count_RoRo) / totalVessels) * 100) });
			if (sv.vessel_count_tanker !== undefined) items.push({ key: 'Tanker Share', value: formatPercent((Number(sv.vessel_count_tanker) / totalVessels) * 100) });
		}
		if (sv.industry_top1) items.push({ key: 'Top Industry', value: sv.industry_top1 });
		if (sv.industry_top2) items.push({ key: 'Top Industry 2', value: sv.industry_top2 });
		if (sv.industry_top3) items.push({ key: 'Top Industry 3', value: sv.industry_top3 });
		if (sv.n_total !== undefined) items.push({ key: 'Total Transits', value: formatCount(sv.n_total) });
		if (sv.n_tanker !== undefined) items.push({ key: 'Tanker Transits', value: formatCount(sv.n_tanker) });
		if (sv.n_container !== undefined) items.push({ key: 'Container Transits', value: formatCount(sv.n_container) });
		if (sv.n_bulk !== undefined) items.push({ key: 'Bulk Carrier Transits', value: formatCount(sv.n_bulk) });
		return items;
	})();

	// Helper to get sparkline values
	function sparkVals(key) {
		if (!pwData || !pwData.timeseries || !pwData.timeseries[key]) return null;
		const pts = pwData.timeseries[key];
		return pts.slice(-30).map(p => p.value);
	}

	// AIS analytics section
	function AISSection() {
		if (aisLoading) return e('div', { className: 'ais-section', key: 'ais-loading' },
			e('div', { className: 'ais-header' }, 'Live AIS Traffic'),
			e('div', { className: 'pw-loading' }, 'Loading AIS data...')
		);
		if (!aisAnalytics) return null;
		const a = aisAnalytics.analytics || {};
		const ships = aisAnalytics.ships || [];
		return e('div', { className: 'ais-section', key: 'ais-data' },
			e('div', { className: 'ais-header' },
				e('span', { className: 'title' }, 'Live AIS Traffic'),
				e('span', { style: { fontSize: '10px', color: 'var(--muted)' } }, a.last_updated ? 'Updated: ' + new Date(a.last_updated).toLocaleTimeString() : '')
			),
			e('div', { className: 'pw-stat-grid' },
				e('div', { className: 'pw-stat' },
					e('div', { className: 'val' }, String(a.unique_ships || 0)),
					e('div', { className: 'lbl' }, 'Ships (10 min)')
				),
				e('div', { className: 'pw-stat' },
					e('div', { className: 'val' }, String(a.underway || 0)),
					e('div', { className: 'lbl' }, 'Underway')
				),
				e('div', { className: 'pw-stat' },
					e('div', { className: 'val' }, String(a.anchored || 0)),
					e('div', { className: 'lbl' }, 'Anchored')
				),
				e('div', { className: 'pw-stat' },
					e('div', { className: 'val' }, (a.avg_speed_knots || 0).toFixed(1)),
					e('div', { className: 'lbl' }, 'Avg kts')
				)
			),
			ships.length > 0 ? e('div', { className: 'ais-ships' },
				e('div', { style: { fontSize: '10px', textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--muted)', marginBottom: '4px', marginTop: '8px' } }, 'Recent ships'),
				ships.slice(0, 10).map(s =>
					e('div', { className: 'ais-ship-row', key: s.mmsi },
						e('span', { className: 'ais-mmsi' }, String(s.mmsi)),
						e('span', { className: 'ais-name', title: s.ship_name || '' }, s.ship_name && s.ship_name.length > 15 ? s.ship_name.slice(0, 15) + '…' : (s.ship_name || '—')),
						e('span', { className: 'ais-speed' }, (s.speed_knots || 0).toFixed(1) + ' kts'),
						e('span', { className: 'ais-cog' }, (s.cog || 0).toFixed(0) + '°')
					)
				)
			) : null
		);
	};

	return e('div', { className: 'shell' },
		e('div', { className: 'topbar' },
			e('div', { className: 'topbar-meta' },
				e('div', { className: 'status-pill' },
					e('span', { className: 'status-dot' }),
					e('span', null, status)
				)
			)
		),
		e('div', { className: 'workspace' },
			e('div', { className: 'sidebar left-rail' },
				e('div', { className: 'panel section-surface' },
					e('div', { className: 'panel-body horizontal', style: { display: 'flex', alignItems: 'center', gap: '12px' } },
						e('div', { style: { flex: '1 1 auto' } },
							e('div', { className: 'panel-title' }, 'Selected record'),
							e('div', { className: 'summary-name' }, selectedData ? selectedData.name : 'Choose a record')
						),
						e('span', { className: 'chip' }, selectedDate ? formatDate(selectedDate) : 'Waiting for selection')
					)
				),
				e('section', { className: 'panel section-surface' },
					e('div', { className: 'panel-header' },
						e('div', { className: 'panel-title' }, '1. Port Metrics')
					),
					e('div', { className: 'panel-body' },
						metrics.length > 0
							? e('div', { className: 'metric-list' }, metrics.map(item => e('div', { key: item.key, className: 'metric-item' }, e('span', null, item.key), e('small', null, String(item.value)))))
							: e('div', { className: 'empty-state' }, 'No record selected.')
					)
				)
			),
			e('div', { className: `map-shell ${mapReady ? 'is-ready' : 'is-loading'} ${mapMode === 'globe' ? 'mode-globe' : 'mode-2d'}` },
				e('div', { className: 'map-header' },
					e('div', { className: 'map-card' },
						e('span', { className: 'eyebrow' }, 'Map view'),
						e('h3', null, mapMode === 'globe' ? '3D globe mode' : 'Fixed 2D map'),
						e('p', null, mapMode === 'globe' ? 'Globe mode is optional. Click a point to load details.' : 'Interactive map with pan, zoom and click. Click a point to load details.'),
						e('div', { style: { display: 'flex', gap: '8px', marginTop: '10px', flexWrap: 'wrap' } },
							e('button', {
								type: 'button',
								onClick: () => setMode('2d'),
								style: {
									padding: '7px 10px',
									borderRadius: '999px',
									border: '1px solid var(--border)',
									background: mapMode === '2d' ? 'rgba(99, 214, 255, 0.18)' : 'rgba(255, 255, 255, 0.04)',
									color: 'var(--text)',
									cursor: 'pointer'
								}
							}, 'Fixed 2D'),
							e('button', {
								type: 'button',
								onClick: () => setMode('globe'),
								style: {
									padding: '7px 10px',
									borderRadius: '999px',
									border: '1px solid var(--border)',
									background: mapMode === 'globe' ? 'rgba(99, 214, 255, 0.18)' : 'rgba(255, 255, 255, 0.04)',
									color: 'var(--text)',
									cursor: 'pointer'
								}
							}, '3D Globe')
						),
						mapLegend
					)
				),
				e('div', { id: 'map' })
			),
	// Right sidebar — PortWatch + AIS analytics
			e('div', { className: 'sidebar right-rail', style: { padding: '12px', gap: '10px' } },
				e('div', { className: 'pw-header' },
					e('span', { className: 'title' }, selectedItem ? selectedItem.data.name : 'PortWatch'),
					selectedItem?.data?.pageid
						? e('a', { className: 'ext-link', href: apiUrl('/portwatch/' + selectedItem.data.pageid), target: '_blank' }, 'Open on PortWatch →')
						: null
				),
				...(!selectedItem
					? [e('div', { className: 'pw-empty', key: 'empty' }, 'Click a port or chokepoint on the map to view PortWatch analytics.')]
					: pwLoading
					? [e('div', { className: 'pw-loading', key: 'loading' }, 'Loading PortWatch data...')]
					: pwError
					? [e('div', { className: 'pw-error', key: 'error' }, 'Could not load PortWatch data. Visit the external link above.')]
					: pwData && pwData.metrics
					? [ e(AISSection, { key: 'ais-section' }),
						e('div', { className: 'pw-stat-grid', key: 'stats' },
						  e('div', { className: 'pw-stat' },
						    e('div', { className: 'val' }, formatCount(pwData.metrics.total_portcalls)),
						    e('div', { className: 'lbl' }, 'Port Calls')
						  ),
							e('div', { className: 'pw-stat' },
								e('div', { className: 'val' }, formatCount(Math.round(pwData.metrics.avg_daily_portcalls))),
								e('div', { className: 'lbl' }, 'Avg Daily')
							),
							e('div', { className: 'pw-stat' },
								e('div', { className: 'val' }, formatCount(Math.round(pwData.metrics.total_imports / 1e6)) + 'M'),
								e('div', { className: 'lbl' }, 'Imports (tons)')
							),
							e('div', { className: 'pw-stat' },
								e('div', { className: 'val' }, formatCount(Math.round(pwData.metrics.total_exports / 1e6)) + 'M'),
								e('div', { className: 'lbl' }, 'Exports (tons)')
							)
						),
						e('div', { style: { fontSize: '10px', color: 'var(--muted)', textAlign: 'center', padding: '2px 0' }, key: 'range' },
							'Data: ' + (pwData.metrics.data_range_start || '?') + ' → ' + (pwData.metrics.data_range_end || '?')
						),
						// Sparkline charts using React component
						e('div', { className: 'pw-chart-box', key: 'c1' },

							e('div', { className: 'pw-chart-label' }, 'Daily Port Calls (last 30)'),
							e(Sparkline, { values: sparkVals('portcalls'), color: '#63d6ff', height: 100 })
						),
						sparkVals('imports') && sparkVals('imports').some(v => v > 0)
							? e('div', { className: 'pw-chart-box', key: 'c2' },
								e('div', { className: 'pw-chart-label' }, 'Daily Imports (tons, last 30)'),
								e(Sparkline, { values: sparkVals('imports'), color: '#76e4b5', height: 80 })
							)
							: null,
						sparkVals('exports') && sparkVals('exports').some(v => v > 0)
							? e('div', { className: 'pw-chart-box', key: 'c3' },
								e('div', { className: 'pw-chart-label' }, 'Daily Exports (tons, last 30)'),
								e(Sparkline, { values: sparkVals('exports'), color: '#ffcc66', height: 80 })
							)
							: null,
						pwData.unavailable_data && pwData.unavailable_data.length > 0
							? e('div', { key: 'ext-links', style: { display: 'flex', flexDirection: 'column', gap: '4px', borderTop: '1px solid var(--border)', paddingTop: '10px', marginTop: '6px' } },
								e('div', { style: { fontSize: '10px', textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--muted)', marginBottom: '4px' } }, 'Additional data on PortWatch'),
								pwData.unavailable_data.map((item, i) =>
									e('a', { key: i, className: 'pw-ext-link', href: item.external_url || pwData.external_url, target: '_blank' },
										e('span', { className: 'icn', style: { color: 'var(--accent)', fontSize: '12px' } }, '↗'),
										e('div', null,
											e('div', null, item.label),
											e('div', { className: 'desc' }, item.description ? item.description.slice(0, 80) + '...' : '')
										)
									)
								)
							)
							: null
						]
					: [e(AISSection, { key: 'ais-section' }), e('div', { className: 'pw-empty', key: 'no-data' }, 'No PortWatch data available for this item.')]
				)
			)
		)
	);
}

	ReactDOM.createRoot(document.getElementById('app')).render(e(App));
})();