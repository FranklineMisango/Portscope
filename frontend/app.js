const e = React.createElement;

function App(){
	const [portId, setPortId] = React.useState(1);
	const [radius, setRadius] = React.useState(5000);
	const mapRef = React.useRef(null);
	const markersRef = React.useRef([]);
	const markerLayerRef = React.useRef(null);
	const [ports, setPorts] = React.useState([]);
	const [history, setHistory] = React.useState([]);
	const chartRef = React.useRef(null);

	React.useEffect(()=>{
		// init map
		mapRef.current = L.map('map').setView([0,0], 2);
		L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png',{
			maxZoom: 19,
		}).addTo(mapRef.current);
		markerLayerRef.current = L.markerClusterGroup();
		mapRef.current.addLayer(markerLayerRef.current);

		// load ports
		fetch('/ports').then(r=>r.json()).then(data=>{ setPorts(data); }).catch(()=>{});
	},[])

	async function fetchLive(){
		try{
			const url = `/port/${portId}/live?radius=${radius}&limit=500`;
			const resp = await fetch(url);
			const data = await resp.json();
			// clear markers
			markersRef.current.forEach(m=>mapRef.current.removeLayer(m));
			markersRef.current = [];
			if(data.length===0) return;
			data.forEach(item=>{
				if(item.position){
					const g = typeof item.position === 'string' ? JSON.parse(item.position) : item.position;
					if(g && g.type==='Point' && g.coordinates){
						const [lon,lat] = g.coordinates;
						const marker = L.marker([lat,lon]);
						marker.bindPopup(`<b>MMSI:</b> ${item.mmsi || 'n/a'}<br/><b>Time:</b> ${item.time}`);
						markerLayerRef.current.addLayer(marker);
						markersRef.current.push(marker);
					}
				}
			});
			// adjust view to markers
			const group = L.featureGroup(markersRef.current);
			if(markersRef.current.length>0){
				mapRef.current.fitBounds(group.getBounds(),{maxZoom:12});
			}
		}catch(err){
			console.error(err);
		}
	}

	React.useEffect(()=>{
		fetchLive();
		const id = setInterval(fetchLive, 10000);
		return ()=>clearInterval(id);
	},[portId,radius]);

	// fetch history for selected port
	React.useEffect(()=>{
		fetch(`/port/${portId}/traffic?range=30d`).then(r=>r.json()).then(data=>{
			setHistory(data);
			// update chart
			const labels = data.map(d=>new Date(d.day).toLocaleDateString());
			const counts = data.map(d=>d.count);
			if(chartRef.current){
				chartRef.current.data.labels = labels;
				chartRef.current.data.datasets[0].data = counts;
				chartRef.current.update();
			} else {
				const ctx = document.getElementById('historyChart').getContext('2d');
				chartRef.current = new Chart(ctx, {
					type: 'line',
					data: { labels, datasets: [{ label: 'Daily traffic', data: counts, borderColor: 'rgba(75,192,192,1)', tension:0.2 }] },
					options: { responsive:true }
				});
			}
		}).catch(()=>{});
	},[portId]);

	return e('div',null,
		e('div',{id:'controls'},
			e('label',null,'Port: '),
			e('select',{value:portId,onChange:e=>setPortId(Number(e.target.value))},
				ports.map(p=>e('option',{key:p.id,value:p.id},p.name))
			),
			' ',
			e('label',null,'Radius(m): '),
			e('input',{type:'number',value:radius,onChange:e=>setRadius(Number(e.target.value))}),
			e('button',{onClick:fetchLive},'Refresh')
		),
		e('div',{id:'map'}),
		e('div',{style:{padding:'8px'}}, e('canvas',{id:'historyChart',width:400,height:100}))
	)
}

ReactDOM.createRoot(document.getElementById('app')).render(e(App));
