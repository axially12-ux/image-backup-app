export namespace core {
	
	export class AISettingsInput {
	    enabled: boolean;
	    key: string;
	    model: string;
	    clearKey: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AISettingsInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.key = source["key"];
	        this.model = source["model"];
	        this.clearKey = source["clearKey"];
	    }
	}
	export class AnalyzeReport {
	    total: number;
	    done: number;
	    aiUsed: number;
	    note: string;
	    ms: number;
	    elapsed: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new AnalyzeReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.done = source["done"];
	        this.aiUsed = source["aiUsed"];
	        this.note = source["note"];
	        this.ms = source["ms"];
	        this.elapsed = source["elapsed"];
	        this.detail = source["detail"];
	    }
	}
	export class CheckReport {
	    dest: string;
	    records: number;
	    verified: number;
	    missing: string[];
	    untracked: string[];
	    ok: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CheckReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dest = source["dest"];
	        this.records = source["records"];
	        this.verified = source["verified"];
	        this.missing = source["missing"];
	        this.untracked = source["untracked"];
	        this.ok = source["ok"];
	    }
	}
	export class ItemResult {
	    name: string;
	    status: string;
	    message: string;
	    method: string;
	
	    static createFrom(source: any = {}) {
	        return new ItemResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.message = source["message"];
	        this.method = source["method"];
	    }
	}
	export class DeleteReport {
	    items: ItemResult[];
	    deleted: number;
	    scope: string;
	
	    static createFrom(source: any = {}) {
	        return new DeleteReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], ItemResult);
	        this.deleted = source["deleted"];
	        this.scope = source["scope"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FileEntry {
	    name: string;
	    size: number;
	    modTime: string;
	
	    static createFrom(source: any = {}) {
	        return new FileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.size = source["size"];
	        this.modTime = source["modTime"];
	    }
	}
	export class Tag {
	    id: number;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new Tag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	    }
	}
	export class Image {
	    id: number;
	    dest: string;
	    filename: string;
	    hash: string;
	    size: number;
	    width: number;
	    height: number;
	    description: string;
	    analyzer: string;
	    tags: Tag[];
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Image(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.dest = source["dest"];
	        this.filename = source["filename"];
	        this.hash = source["hash"];
	        this.size = source["size"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.description = source["description"];
	        this.analyzer = source["analyzer"];
	        this.tags = this.convertValues(source["tags"], Tag);
	        this.createdAt = this.convertValues(source["createdAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class MoveReport {
	    items: ItemResult[];
	    moved: number;
	    skipped: number;
	    failed: number;
	    moveMs: number;
	    analyzeMs: number;
	    totalMs: number;
	    elapsed: string;
	    mode: string;
	    analyzed: number;
	    aiUsed: number;
	    note: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new MoveReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], ItemResult);
	        this.moved = source["moved"];
	        this.skipped = source["skipped"];
	        this.failed = source["failed"];
	        this.moveMs = source["moveMs"];
	        this.analyzeMs = source["analyzeMs"];
	        this.totalMs = source["totalMs"];
	        this.elapsed = source["elapsed"];
	        this.mode = source["mode"];
	        this.analyzed = source["analyzed"];
	        this.aiUsed = source["aiUsed"];
	        this.note = source["note"];
	        this.detail = source["detail"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class State {
	    source: string;
	    dest: string;
	    sourceCount: number;
	    aiEnabled: boolean;
	    hasAiKey: boolean;
	    aiModel: string;
	    dbPath: string;
	    restored: boolean;
	    integrityNote: string;
	
	    static createFrom(source: any = {}) {
	        return new State(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.dest = source["dest"];
	        this.sourceCount = source["sourceCount"];
	        this.aiEnabled = source["aiEnabled"];
	        this.hasAiKey = source["hasAiKey"];
	        this.aiModel = source["aiModel"];
	        this.dbPath = source["dbPath"];
	        this.restored = source["restored"];
	        this.integrityNote = source["integrityNote"];
	    }
	}
	
	export class TagCount {
	    name: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new TagCount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.count = source["count"];
	    }
	}

}

