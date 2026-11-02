import { useId, useState } from 'react'
import { Area, AreaChart, Brush, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import type { TimeSeriesPoint } from '../types'
const series = [
  { key: 'rate200', label: '200 · Successful', color: '#32f59b' },
  { key: 'rate401', label: '401 · Unauthorized', color: '#ffb0b8' },
  { key: 'rate429', label: '429 · Rate limited', color: '#ff657a' },
  { key: 'rate5xx', label: '5xx · Server errors', color: '#ff304f' },
] as const
export function TrafficChart({ points }: { points: TimeSeriesPoint[] | null }) {
 const id = useId().replace(/:/g, '')
 const [minutes, setMinutes] = useState(15)
 const [hidden, setHidden] = useState<string[]>([])
 const all = points || []
 const end = all.at(-1)?.timestamp || 0
 const data = all.filter(p => p.timestamp >= end - minutes * 60)
 const latest = data.at(-1)
 const peak = data.length ? Math.max(...data.map(p => p.totalRate)) : null
 return <section className="traffic-panel">
   <div className="traffic-heading"><div><div className="eyebrow">TRAFFIC INTELLIGENCE</div><h3>Every request, in real time.</h3><p>Requests per second · {all[0]?.isFallback ? 'Calculated from browser polling' : 'Prometheus · 1-minute rate'}</p></div><div className="chart-period" aria-label="Chart time window">{[1,5,15].map(n => <button key={n} aria-pressed={minutes === n} onClick={() => setMinutes(n)}>{n}m</button>)}</div></div>
   <div className="chart-stats"><div><small>LATEST THROUGHPUT</small><strong>{latest ? latest.totalRate.toFixed(3) : '—'} <span>req/s</span></strong></div><div><small>PEAK IN VIEW</small><strong>{peak !== null ? peak.toFixed(3) : '—'} <span>req/s</span></strong></div><div><small>DATA POINTS</small><strong>{data.length}<span> samples</span></strong></div></div>
   <div className="chart-toggles">{series.map(s => <button key={s.key} aria-pressed={!hidden.includes(s.key)} onClick={() => setHidden(h => h.includes(s.key) ? h.filter(k => k !== s.key) : [...h,s.key])}><i style={{ background: s.color }}/>{s.label}</button>)}</div>
   {data.length ? <><div className="interactive-chart"><ResponsiveContainer width="100%" height="100%"><AreaChart data={data} margin={{top:12,right:15,left:0,bottom:0}} accessibilityLayer>
    <defs>{series.map(s => <linearGradient key={s.key} id={`${id}-${s.key}`} x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor={s.color} stopOpacity={.28}/><stop offset="100%" stopColor={s.color} stopOpacity={0}/></linearGradient>)}</defs>
    <CartesianGrid vertical={false} stroke="#2a2a2e" strokeDasharray="3 6"/>
    <XAxis dataKey="time" stroke="#a3a3ac" fontSize={10} minTickGap={45} tickLine={false}/><YAxis stroke="#a3a3ac" fontSize={10} width={45} tickLine={false} axisLine={false} domain={[0,'auto']}/>
    <Tooltip contentStyle={{background:'#08090b',border:'1px solid #45454d',borderRadius:10,fontSize:12,color:'#fff'}} labelStyle={{color:'#fff',marginBottom:8}} formatter={(value, name) => [`${Number(value).toFixed(3)} req/s`,name]} cursor={{stroke:'#9ca3af',strokeDasharray:'4 4'}}/>
    {series.map(s => <Area key={s.key} hide={hidden.includes(s.key)} type="monotone" dataKey={s.key} name={s.label} stroke={s.color} strokeWidth={2.5} fill={`url(#${id}-${s.key})`} activeDot={{r:5,stroke:'#050505',strokeWidth:2}} isAnimationActive={false}/>)}
    <Brush key={minutes} dataKey="time" height={24} stroke="#3e6d53" fill="#080c0a" travellerWidth={9} tickFormatter={() => ''}/>
   </AreaChart></ResponsiveContainer></div><div className="chart-footnote"><span>Hover to inspect · Click a status to filter · Drag the handles to zoom</span><span>{data.every(p => p.totalRate === 0) ? 'No traffic recorded in this window' : `${data[0].time} — ${latest?.time}`}</span></div></> : <div className="chart-empty">Waiting for traffic samples. Send a request from the API playground to begin.</div>}
 </section>
}
