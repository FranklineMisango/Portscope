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
		return parsed.coordinates;
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
			id: properties.ObjectId || properties.portid || index + 1,
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
	const [status, setStatus] = React.useState('Loading PortWatch datasets...');
	const [pwData, setPwData] = React.useState(null);
	const [pwLoading, setPwLoading] = React.useState(false);
	const [pwError, setPwError] = React.useState(null);

	const mapRef = React.useRef(null);
	const portLayerRef = React.useRef(null);
	const chokepointLayerRef = React.useRef(null);
	const pwPrevPageid = React.useRef(null);

	function selectItem(type, data) {
		setSelectedItem({ type, data });
		setStatus(`${data.name} selected.`);
	}

	// Fetch PortWatch data
	React.useEffect(() => {
		const pageid = selectedItem?.data?.pageid;
		if (!pageid) {
			setPwData(null);
			setPwLoading(false);
			setPwError(null);
			pwPrevPageid.current = null;
			return;
		}
		if (pageid === pwPrevPageid.current) return;
		pwPrevPageid.current = pageid;

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
	}, [selectedItem]);

	function renderLayers() {
		if (!portLayerRef.current || !chokepointLayerRef.current) return;
		portLayerRef.current.clearLayers();
		chokepointLayerRef.current.clearLayers();

		const hasSelection = Boolean(selectedItem);

		ports.forEach(item => {
			const coords = pointFromGeometry(item.geom);
			if (!coords) return;
			const [lon, lat] = coords;
			const active = hasSelection && selectedItem.type === 'port' && selectedItem.data.id === item.id;
			const marker = L.circleMarker([lat, lon], {
				radius: active ? 10 : 7,
				color: active ? '#63d6ff' : '#8a7dff',
				weight: 2,
				fillColor: active ? '#63d6ff' : '#c9c1ff',
				fillOpacity: 0.92,
			});
			marker.bindTooltip(`<b>${item.name}</b><br/>Port intelligence`, { direction: 'top', offset: [0, -8] });
			marker.on('click', () => selectItem('port', item));
			marker.addTo(portLayerRef.current);
		});

		chokepoints.forEach(item => {
			const coords = pointFromGeometry(item.geom);
			if (!coords) return;
			const [lon, lat] = coords;
			const active = hasSelection && selectedItem.type === 'chokepoint' && selectedItem.data.id === item.id;
			const marker = L.circleMarker([lat, lon], {
				radius: active ? 9 : 6,
				color: active ? '#ffcc66' : '#ff8a5b',
				weight: 2,
				fillColor: active ? '#ffcc66' : '#ffb38f',
				fillOpacity: 0.95,
			});
			marker.bindTooltip(`<b>${item.name}</b><br/>Chokepoint intensity`, { direction: 'top', offset: [0, -8] });
			marker.on('click', () => selectItem('chokepoint', item));
			marker.addTo(chokepointLayerRef.current);
		});
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
		if (typeof L === 'undefined') {
			setStatus('Leaflet library not loaded; map unavailable.');
			return;
		}
		setTimeout(() => {
			const mapEl = document.getElementById('map');
			if (!mapEl || mapEl._leaflet_id) return;
			mapRef.current = L.map('map', { zoomControl: true, worldCopyJump: true }).setView([20, 20], 2);
			L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
				maxZoom: 19,
				attribution: '&copy; OpenStreetMap contributors &copy; CARTO',
			}).addTo(mapRef.current);
			portLayerRef.current = L.layerGroup().addTo(mapRef.current);
			chokepointLayerRef.current = L.layerGroup().addTo(mapRef.current);
			loadData();
		}, 50);
		return () => {
			if (mapRef.current) { mapRef.current.remove(); mapRef.current = null; }
		};
	}, []);

	const dataLoadedRef = React.useRef(false);
	React.useEffect(() => {
		if (!mapRef.current) return;
		if ((ports.length === 0 && chokepoints.length === 0)) return;
		renderLayers();
		if (!dataLoadedRef.current) {
			dataLoadedRef.current = true;
			setTimeout(() => {
				if (!mapRef.current) return;
				mapRef.current.invalidateSize();
				const allCoords = [];
				ports.forEach(p => { const c = pointFromGeometry(p.geom); if (c) allCoords.push([c[1], c[0]]); });
				chokepoints.forEach(p => { const c = pointFromGeometry(p.geom); if (c) allCoords.push([c[1], c[0]]); });
				mapRef.current.fitBounds(allCoords, { padding: [30, 30], maxZoom: 4 });
			}, 200);
		}
	}, [ports, chokepoints]);

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
			e('div', { className: 'map-shell' },
				e('div', { className: 'map-header' },
					e('div', { className: 'map-card' },
						e('span', { className: 'eyebrow' }, 'Map view'),
						e('h3', null, 'Global port activity'),
						e('p', null, 'Click a point to load details.')
					)
				),
				e('div', { id: 'map' })
			),
			// Right sidebar — PortWatch analytics (wider)
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
					? [
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
					: [e('div', { className: 'pw-empty', key: 'no-data' }, 'No PortWatch data available for this item.')]
				)
			)
		)
	);
}

	ReactDOM.createRoot(document.getElementById('app')).render(e(App));
})();