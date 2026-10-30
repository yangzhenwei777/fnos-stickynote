export namespace bootstrap {
	
	export class PhaseState {
	    phase: string;
	    since_ms: number;
	
	    static createFrom(source: any = {}) {
	        return new PhaseState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.phase = source["phase"];
	        this.since_ms = source["since_ms"];
	    }
	}

}

export namespace main {
	
	export class ConflictCopyResult {
	    note?: model.Note;
	    folder_name: string;
	    folder_fallback: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConflictCopyResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.note = this.convertValues(source["note"], model.Note);
	        this.folder_name = source["folder_name"];
	        this.folder_fallback = source["folder_fallback"];
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
	export class ConflictPage {
	    items: synclocal.Conflict[];
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new ConflictPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], synclocal.Conflict);
	        this.total = source["total"];
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
	export class OutboxPage {
	    items: synclocal.OutboxOp[];
	    counts: Record<string, number>;
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new OutboxPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], synclocal.OutboxOp);
	        this.counts = source["counts"];
	        this.total = source["total"];
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
	export class UploadResult {
	    filename: string;
	    url: string;
	
	    static createFrom(source: any = {}) {
	        return new UploadResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.url = source["url"];
	    }
	}

}

export namespace model {
	
	export class Folder {
	    id?: number;
	    name: string;
	    sort_order: number;
	    version: number;
	    note_count: number;
	    is_default: boolean;
	    is_encrypted: boolean;
	    // Go type: time
	    created_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Folder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.sort_order = source["sort_order"];
	        this.version = source["version"];
	        this.note_count = source["note_count"];
	        this.is_default = source["is_default"];
	        this.is_encrypted = source["is_encrypted"];
	        this.created_at = this.convertValues(source["created_at"], null);
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
	export class Note {
	    id: number;
	    user_id: number;
	    title: string;
	    content: string;
	    tags: string;
	    is_encrypted: boolean;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	    // Go type: time
	    deleted_at?: any;
	    highlight?: string;
	    version: number;
	    folder_id?: number;
	
	    static createFrom(source: any = {}) {
	        return new Note(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.user_id = source["user_id"];
	        this.title = source["title"];
	        this.content = source["content"];
	        this.tags = source["tags"];
	        this.is_encrypted = source["is_encrypted"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	        this.deleted_at = this.convertValues(source["deleted_at"], null);
	        this.highlight = source["highlight"];
	        this.version = source["version"];
	        this.folder_id = source["folder_id"];
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
	export class NoteListResponse {
	    notes: Note[];
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new NoteListResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.notes = this.convertValues(source["notes"], Note);
	        this.total = source["total"];
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

}

export namespace synclocal {
	
	export class Conflict {
	    id: number;
	    entity_sync_id: string;
	    entity_type: string;
	    operation: string;
	    op_id: string;
	    kind: string;
	    local_payload: string;
	    remote_payload: string;
	    base_version: number;
	    remote_version: number;
	    created_at: string;
	    status: string;
	    resolution: string;
	    resolved_at: string;
	    open_ops: number;
	    allowed_resolutions: string[];
	
	    static createFrom(source: any = {}) {
	        return new Conflict(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.entity_sync_id = source["entity_sync_id"];
	        this.entity_type = source["entity_type"];
	        this.operation = source["operation"];
	        this.op_id = source["op_id"];
	        this.kind = source["kind"];
	        this.local_payload = source["local_payload"];
	        this.remote_payload = source["remote_payload"];
	        this.base_version = source["base_version"];
	        this.remote_version = source["remote_version"];
	        this.created_at = source["created_at"];
	        this.status = source["status"];
	        this.resolution = source["resolution"];
	        this.resolved_at = source["resolved_at"];
	        this.open_ops = source["open_ops"];
	        this.allowed_resolutions = source["allowed_resolutions"];
	    }
	}
	export class OutboxOp {
	    seq: number;
	    op_id: string;
	    entity_type: string;
	    entity_sync_id: string;
	    operation: string;
	    base_version?: number;
	    status: string;
	    retry_count: number;
	    last_error: string;
	    created_at: string;
	    updated_at: string;
	    payload_bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new OutboxOp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.op_id = source["op_id"];
	        this.entity_type = source["entity_type"];
	        this.entity_sync_id = source["entity_sync_id"];
	        this.operation = source["operation"];
	        this.base_version = source["base_version"];
	        this.status = source["status"];
	        this.retry_count = source["retry_count"];
	        this.last_error = source["last_error"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.payload_bytes = source["payload_bytes"];
	    }
	}
	export class ResolveResult {
	    conflict_id: number;
	    kind: string;
	    entity_type: string;
	    entity_sync_id: string;
	    resolution: string;
	    requeued_ops: number;
	    terminated_ops: number;
	    applied_remote: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ResolveResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conflict_id = source["conflict_id"];
	        this.kind = source["kind"];
	        this.entity_type = source["entity_type"];
	        this.entity_sync_id = source["entity_sync_id"];
	        this.resolution = source["resolution"];
	        this.requeued_ops = source["requeued_ops"];
	        this.terminated_ops = source["terminated_ops"];
	        this.applied_remote = source["applied_remote"];
	    }
	}

}

