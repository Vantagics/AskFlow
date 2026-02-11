// ============================================================
// SPA Router & App State
// ============================================================

(function () {
    'use strict';

    var SESSION_KEY = 'helpdesk_session';
    var USER_KEY = 'helpdesk_user';
    var adminLoginRoute = '/admin'; // default, will be fetched from server
    var loginCaptchaId = '';
    var registerCaptchaId = '';

    // --- Routing ---

    function getRoute() {
        var path = window.location.pathname || '/';
        // Normalize: remove trailing slash (except root)
        if (path.length > 1 && path.charAt(path.length - 1) === '/') {
            path = path.slice(0, -1);
        }
        return path;
    }

    function navigate(route) {
        window.history.pushState({}, '', route);
        handleRoute();
    }

    function showPage(pageId) {
        var pages = document.querySelectorAll('.page');
        pages.forEach(function (p) { p.classList.add('hidden'); });
        var target = document.getElementById('page-' + pageId);
        if (target) {
            target.classList.remove('hidden');
        }
    }

    function handleRoute() {
        var route = getRoute();
        var session = getSession();
        var user = getUser();
        var isAdmin = session && user && user.provider === 'admin';

        if (route === adminLoginRoute) {
            if (isAdmin) {
                showPage('admin');
                initAdmin();
            } else {
                showPage('admin-login');
                initAdminLogin();
            }
        } else if (route === '/admin-panel') {
            if (isAdmin) {
                showPage('admin');
                initAdmin();
            } else {
                navigate(adminLoginRoute);
            }
        } else if (route === '/login') {
            if (session) {
                navigate('/chat');
            } else {
                showPage('login');
                loadLoginCaptcha();
            }
        } else if (route === '/register') {
            if (session) {
                navigate('/chat');
            } else {
                showPage('login');
                showRegisterForm();
            }
        } else if (route === '/verify') {
            showPage('verify');
            handleEmailVerify();
        } else if (route === '/chat' || route === '/') {
            if (!session) {
                showPage('login');
                loadLoginCaptcha();
            } else {
                showPage('chat');
                initChat();
            }
        } else {
            if (!session) {
                showPage('login');
                loadLoginCaptcha();
            } else {
                showPage('chat');
                initChat();
            }
        }
    }

    window.addEventListener('popstate', handleRoute);

    // --- Session Management ---

    function getSession() {
        try {
            var data = localStorage.getItem(SESSION_KEY);
            if (!data) return null;
            var session = JSON.parse(data);
            if (session.expires_at && new Date(session.expires_at) < new Date()) {
                clearSession();
                return null;
            }
            return session;
        } catch (e) {
            return null;
        }
    }

    function saveSession(session, user) {
        localStorage.setItem(SESSION_KEY, JSON.stringify(session));
        if (user) {
            localStorage.setItem(USER_KEY, JSON.stringify(user));
        }
    }

    function clearSession() {
        localStorage.removeItem(SESSION_KEY);
        localStorage.removeItem(USER_KEY);
    }

    function getUser() {
        try {
            var data = localStorage.getItem(USER_KEY);
            if (!data) return null;
            return JSON.parse(data);
        } catch (e) {
            return null;
        }
    }

    // --- Toast Notifications ---

    var toastTimer = null;

    function showToast(message, type) {
        type = type || 'info';
        var toast = document.getElementById('login-toast');
        if (!toast) return;
        toast.textContent = message;
        toast.className = 'toast toast-' + type;
        if (toastTimer) clearTimeout(toastTimer);
        toastTimer = setTimeout(function () {
            toast.classList.add('hidden');
        }, 3000);
    }

    // --- Captcha ---

    function loadCaptcha(questionElId, storeKey) {
        fetch('/api/captcha')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                var el = document.getElementById(questionElId);
                if (el) el.textContent = data.question;
                if (storeKey === 'login') loginCaptchaId = data.id;
                else registerCaptchaId = data.id;
            })
            .catch(function () {
                var el = document.getElementById(questionElId);
                if (el) el.textContent = '加载失败，点击重试';
            });
    }

    window.loadLoginCaptcha = function () {
        loadCaptcha('user-login-captcha-question', 'login');
    };

    window.loadRegisterCaptcha = function () {
        loadCaptcha('user-register-captcha-question', 'register');
    };

    // --- User Login & Register ---

    window.showLoginForm = function () {
        var loginForm = document.getElementById('user-login-form');
        var registerForm = document.getElementById('user-register-form');
        if (loginForm) loginForm.classList.remove('hidden');
        if (registerForm) registerForm.classList.add('hidden');
        loadLoginCaptcha();
    };

    window.showRegisterForm = function () {
        var loginForm = document.getElementById('user-login-form');
        var registerForm = document.getElementById('user-register-form');
        if (loginForm) loginForm.classList.add('hidden');
        if (registerForm) registerForm.classList.remove('hidden');
        loadRegisterCaptcha();
    };

    window.handleUserLogin = function () {
        var emailInput = document.getElementById('user-login-email');
        var passwordInput = document.getElementById('user-login-password');
        var captchaInput = document.getElementById('user-login-captcha');
        var errorEl = document.getElementById('user-login-error');
        var submitBtn = document.querySelector('#user-login-form .admin-submit-btn');

        if (!emailInput || !passwordInput) return;
        var email = emailInput.value.trim();
        var password = passwordInput.value;
        var captchaAnswer = captchaInput ? parseInt(captchaInput.value.trim(), 10) : 0;

        if (!email || !password) {
            if (errorEl) { errorEl.textContent = '请输入邮箱和密码'; errorEl.classList.remove('hidden'); }
            return;
        }
        if (!captchaInput || !captchaInput.value.trim()) {
            if (errorEl) { errorEl.textContent = '请输入验证码'; errorEl.classList.remove('hidden'); }
            return;
        }
        if (errorEl) errorEl.classList.add('hidden');
        if (submitBtn) submitBtn.disabled = true;

        fetch('/api/auth/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: email, password: password, captcha_id: loginCaptchaId, captcha_answer: captchaAnswer })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '登录失败'); });
            return res.json();
        })
        .then(function (data) {
            if (data.session) {
                saveSession(data.session, data.user);
                navigate('/chat');
            }
        })
        .catch(function (err) {
            if (errorEl) { errorEl.textContent = err.message; errorEl.classList.remove('hidden'); }
            if (captchaInput) captchaInput.value = '';
            loadLoginCaptcha();
        })
        .finally(function () {
            if (submitBtn) submitBtn.disabled = false;
        });
    };

    window.handleUserRegister = function () {
        var nameInput = document.getElementById('user-register-name');
        var emailInput = document.getElementById('user-register-email');
        var passwordInput = document.getElementById('user-register-password');
        var confirmInput = document.getElementById('user-register-password-confirm');
        var captchaInput = document.getElementById('user-register-captcha');
        var errorEl = document.getElementById('user-register-error');
        var successEl = document.getElementById('user-register-success');
        var submitBtn = document.querySelector('#user-register-form .admin-submit-btn');

        if (!emailInput || !passwordInput || !confirmInput) return;
        var name = nameInput ? nameInput.value.trim() : '';
        var email = emailInput.value.trim();
        var password = passwordInput.value;
        var confirm = confirmInput.value;
        var captchaAnswer = captchaInput ? parseInt(captchaInput.value.trim(), 10) : 0;

        if (!email) { if (errorEl) { errorEl.textContent = '请输入邮箱'; errorEl.classList.remove('hidden'); } return; }
        if (!password) { if (errorEl) { errorEl.textContent = '请输入密码'; errorEl.classList.remove('hidden'); } return; }
        if (password.length < 6) { if (errorEl) { errorEl.textContent = '密码至少6位'; errorEl.classList.remove('hidden'); } return; }
        if (password !== confirm) { if (errorEl) { errorEl.textContent = '两次密码不一致'; errorEl.classList.remove('hidden'); } return; }
        if (!captchaInput || !captchaInput.value.trim()) { if (errorEl) { errorEl.textContent = '请输入验证码'; errorEl.classList.remove('hidden'); } return; }

        if (errorEl) errorEl.classList.add('hidden');
        if (successEl) successEl.classList.add('hidden');
        if (submitBtn) submitBtn.disabled = true;

        fetch('/api/auth/register', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: email, name: name, password: password, captcha_id: registerCaptchaId, captcha_answer: captchaAnswer })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '注册失败'); });
            return res.json();
        })
        .then(function (data) {
            if (successEl) { successEl.textContent = data.message || '注册成功，请查收验证邮件'; successEl.classList.remove('hidden'); }
            if (errorEl) errorEl.classList.add('hidden');
        })
        .catch(function (err) {
            if (errorEl) { errorEl.textContent = err.message; errorEl.classList.remove('hidden'); }
            if (captchaInput) captchaInput.value = '';
            loadRegisterCaptcha();
        })
        .finally(function () {
            if (submitBtn) submitBtn.disabled = false;
        });
    };

    function handleEmailVerify() {
        var params = new URLSearchParams(window.location.search);
        var token = params.get('token');
        var statusEl = document.getElementById('verify-status');

        if (!token) {
            if (statusEl) statusEl.innerHTML = '<p class="error-text">无效的验证链接</p>';
            return;
        }

        fetch('/api/auth/verify?token=' + encodeURIComponent(token))
            .then(function (res) {
                if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '验证失败'); });
                return res.json();
            })
            .then(function (data) {
                if (statusEl) {
                    statusEl.innerHTML = '<p class="success-text">' + escapeHtml(data.message || '邮箱验证成功') + '</p>' +
                        '<p style="margin-top:1rem;"><a href="/login">前往登录</a></p>';
                }
            })
            .catch(function (err) {
                if (statusEl) statusEl.innerHTML = '<p class="error-text">' + escapeHtml(err.message) + '</p>';
            });
    }

    // Enter key for login/register forms
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') {
            var el = document.activeElement;
            if (el && (el.id === 'user-login-email' || el.id === 'user-login-password' || el.id === 'user-login-captcha')) {
                window.handleUserLogin();
            }
            if (el && (el.id === 'user-register-name' || el.id === 'user-register-email' || el.id === 'user-register-password' || el.id === 'user-register-password-confirm' || el.id === 'user-register-captcha')) {
                window.handleUserRegister();
            }
        }
    });

    // --- Admin Login Page ---

    function initAdminLogin() {
        fetch('/api/admin/status')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                var loginForm = document.getElementById('admin-login-form');
                var setupForm = document.getElementById('admin-setup-form');
                if (data.configured) {
                    if (loginForm) loginForm.classList.remove('hidden');
                    if (setupForm) setupForm.classList.add('hidden');
                    var input = document.getElementById('admin-username');
                    if (input) input.focus();
                } else {
                    if (loginForm) loginForm.classList.add('hidden');
                    if (setupForm) setupForm.classList.remove('hidden');
                    var input = document.getElementById('admin-setup-username');
                    if (input) input.focus();
                }
            })
            .catch(function () {
                var loginForm = document.getElementById('admin-login-form');
                if (loginForm) loginForm.classList.remove('hidden');
            });
    }

    window.handleAdminSetup = function () {
        var usernameInput = document.getElementById('admin-setup-username');
        var passwordInput = document.getElementById('admin-setup-password');
        var confirmInput = document.getElementById('admin-setup-password-confirm');
        var errorEl = document.getElementById('admin-setup-error');
        var submitBtn = document.querySelector('#admin-setup-form .admin-submit-btn');

        if (!usernameInput || !passwordInput || !confirmInput) return;

        var username = usernameInput.value.trim();
        var password = passwordInput.value;
        var confirm = confirmInput.value;

        if (!username) {
            if (errorEl) { errorEl.textContent = '请输入用户名'; errorEl.classList.remove('hidden'); }
            return;
        }
        if (!password) {
            if (errorEl) { errorEl.textContent = '请输入密码'; errorEl.classList.remove('hidden'); }
            return;
        }
        if (password.length < 6) {
            if (errorEl) { errorEl.textContent = '密码至少6位'; errorEl.classList.remove('hidden'); }
            return;
        }
        if (password !== confirm) {
            if (errorEl) { errorEl.textContent = '两次密码不一致'; errorEl.classList.remove('hidden'); }
            return;
        }

        if (errorEl) errorEl.classList.add('hidden');
        if (submitBtn) submitBtn.disabled = true;

        fetch('/api/admin/setup', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username, password: password })
        })
            .then(function (res) {
                if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '设置失败'); });
                return res.json();
            })
            .then(function (data) {
                if (data.session) {
                    saveSession(data.session, { name: username, provider: 'admin' });
                    if (data.role) localStorage.setItem('admin_role', data.role);
                    navigate('/admin-panel');
                } else {
                    throw new Error('设置失败');
                }
            })
            .catch(function (err) {
                if (errorEl) { errorEl.textContent = err.message || '设置失败'; errorEl.classList.remove('hidden'); }
            })
            .finally(function () {
                if (submitBtn) submitBtn.disabled = false;
            });
    };

    window.handleAdminLogin = function () {
        var usernameInput = document.getElementById('admin-username');
        var input = document.getElementById('admin-password');
        var errorEl = document.getElementById('admin-login-error');
        var submitBtn = document.querySelector('#admin-login-form .admin-submit-btn');

        if (!usernameInput || !input) return;

        var username = usernameInput.value.trim();
        var password = input.value.trim();
        if (!username || !password) {
            if (errorEl) {
                errorEl.textContent = '请输入用户名和密码';
                errorEl.classList.remove('hidden');
            }
            return;
        }

        if (errorEl) errorEl.classList.add('hidden');
        if (submitBtn) submitBtn.disabled = true;

        fetch('/api/admin/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username, password: password })
        })
            .then(function (res) {
                if (!res.ok) {
                    if (res.status === 401 || res.status === 403) {
                        throw new Error('用户名或密码错误');
                    }
                    throw new Error('登录失败');
                }
                return res.json();
            })
            .then(function (data) {
                if (data.session) {
                    saveSession(data.session, { name: username, provider: 'admin' });
                    if (data.role) localStorage.setItem('admin_role', data.role);
                    navigate('/admin-panel');
                } else {
                    throw new Error('登录失败');
                }
            })
            .catch(function (err) {
                if (errorEl) {
                    errorEl.textContent = err.message || '登录失败，请重试';
                    errorEl.classList.remove('hidden');
                }
                input.value = '';
                input.focus();
            })
            .finally(function () {
                if (submitBtn) submitBtn.disabled = false;
            });
    };

    // Allow Enter key to submit admin login/setup
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') {
            var el = document.activeElement;
            if (el && (el.id === 'admin-username' || el.id === 'admin-password')) {
                window.handleAdminLogin();
            }
            if (el && (el.id === 'admin-setup-username' || el.id === 'admin-setup-password' || el.id === 'admin-setup-password-confirm')) {
                window.handleAdminSetup();
            }
        }
    });

    // --- Chat ---

    var chatMessages = [];
    var chatLoading = false;
    var chatPendingImage = null; // base64 data URL of pasted image

    function getChatUserID() {
        try {
            var user = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
            return user.id || user.email || 'anonymous';
        } catch (e) {
            return 'anonymous';
        }
    }

    function getChatToken() {
        var session = getSession();
        return session ? session.id || session.session_id || '' : '';
    }

    function initChat() {
        var nameEl = document.getElementById('chat-user-name');
        var loginBtn = document.getElementById('chat-login-btn');
        var logoutBtn = document.getElementById('chat-logout-btn');
        var session = getSession();

        if (nameEl) {
            try {
                var user = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
                nameEl.textContent = user.name || user.email || '';
            } catch (e) { /* ignore */ }
        }
        if (session) {
            if (loginBtn) loginBtn.classList.add('hidden');
            if (logoutBtn) logoutBtn.classList.remove('hidden');
        } else {
            if (loginBtn) loginBtn.classList.remove('hidden');
            if (logoutBtn) logoutBtn.classList.add('hidden');
        }

        // Load product intro as welcome message if no messages yet
        if (chatMessages.length === 0) {
            fetch('/api/product-intro')
                .then(function (res) { return res.json(); })
                .then(function (data) {
                    if (data.product_intro) {
                        chatMessages.push({
                            role: 'system',
                            content: data.product_intro,
                            sources: [],
                            isPending: false,
                            timestamp: Date.now()
                        });
                    }
                    renderChatMessages();
                })
                .catch(function () {
                    renderChatMessages();
                });
        } else {
            renderChatMessages();
        }
        setupChatInput();
    }

    function setupChatInput() {
        var input = document.getElementById('chat-input');
        var sendBtn = document.getElementById('chat-send-btn');
        if (!input) return;

        input.addEventListener('input', function () {
            // Auto-grow textarea
            this.style.height = 'auto';
            this.style.height = Math.min(this.scrollHeight, 120) + 'px';
            // Enable/disable send button
            updateSendBtnState();
        });

        input.addEventListener('keydown', function (e) {
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                if ((this.value.trim() || chatPendingImage) && !chatLoading) {
                    window.sendChatMessage();
                }
            }
        });

        // Paste image from clipboard
        input.addEventListener('paste', function (e) {
            var items = (e.clipboardData || e.originalEvent.clipboardData || {}).items;
            if (!items) return;
            for (var i = 0; i < items.length; i++) {
                if (items[i].type.indexOf('image') !== -1) {
                    e.preventDefault();
                    var file = items[i].getAsFile();
                    if (!file) continue;
                    var reader = new FileReader();
                    reader.onload = function (ev) {
                        chatPendingImage = ev.target.result;
                        showChatImagePreview(chatPendingImage);
                        updateSendBtnState();
                    };
                    reader.readAsDataURL(file);
                    break;
                }
            }
        });
    }

    function updateSendBtnState() {
        var input = document.getElementById('chat-input');
        var sendBtn = document.getElementById('chat-send-btn');
        if (sendBtn) {
            sendBtn.disabled = (!(input && input.value.trim()) && !chatPendingImage) || chatLoading;
        }
    }

    function showChatImagePreview(dataUrl) {
        var preview = document.getElementById('chat-image-preview');
        var img = document.getElementById('chat-image-preview-img');
        if (preview && img) {
            img.src = dataUrl;
            preview.classList.remove('hidden');
        }
    }

    window.removeChatImage = function () {
        chatPendingImage = null;
        var preview = document.getElementById('chat-image-preview');
        if (preview) preview.classList.add('hidden');
        updateSendBtnState();
    };

    function renderChatMessages() {
        var container = document.getElementById('chat-messages');
        if (!container) return;

        if (chatMessages.length === 0 && !chatLoading) {
            container.innerHTML =
                '<div class="chat-welcome">' +
                    '<svg width="48" height="48" viewBox="0 0 48 48" fill="none">' +
                        '<rect width="48" height="48" rx="12" fill="#4F46E5" opacity="0.1"/>' +
                        '<path d="M16 20h16M16 24h12M16 28h14M14 16h20a2 2 0 012 2v12a2 2 0 01-2 2H14a2 2 0 01-2-2V18a2 2 0 012-2z" stroke="#4F46E5" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>' +
                    '</svg>' +
                    '<h3>欢迎使用软件自助服务平台</h3>' +
                    '<p>请输入您的问题，我将为您查找相关资料并提供解答。</p>' +
                '</div>';
            return;
        }

        var html = '';
        for (var i = 0; i < chatMessages.length; i++) {
            html += renderSingleMessage(chatMessages[i]);
        }
        if (chatLoading) {
            html += renderLoadingIndicator();
        }
        container.innerHTML = html;
        scrollChatToBottom();
    }

    function renderSingleMessage(msg) {
        var timeStr = formatTime(msg.timestamp);

        if (msg.role === 'user') {
            var userHtml = '<div class="chat-msg chat-msg-user">' +
                '<div class="chat-msg-bubble">' + escapeHtml(msg.content);
            if (msg.imageUrl) {
                userHtml += '<div class="chat-msg-user-image"><img src="' + msg.imageUrl + '" alt="用户图片" /></div>';
            }
            userHtml += '</div>' +
                '<span class="chat-msg-time">' + timeStr + '</span>' +
            '</div>';
            return userHtml;
        }

        // System message
        var extraClass = msg.isPending ? ' chat-msg-pending' : '';
        var html = '<div class="chat-msg chat-msg-system' + extraClass + '">';
        html += '<div class="chat-msg-bubble">';

        if (msg.isPending) {
            html += '<span class="pending-icon">⏳</span>';
        }
        html += escapeHtml(msg.content);

        // Display images from sources inline
        if (msg.sources && msg.sources.length > 0) {
            for (var k = 0; k < msg.sources.length; k++) {
                if (msg.sources[k].image_url) {
                    html += '<div class="chat-msg-image"><img src="' + escapeHtml(msg.sources[k].image_url) + '" alt="' + escapeHtml(msg.sources[k].document_name || '图片') + '" loading="lazy" style="max-width:100%;border-radius:8px;margin-top:8px;cursor:pointer;" onclick="window.open(this.src,\'_blank\')" /></div>';
                }
            }
        }
        html += '</div>';

        // Sources
        if (msg.sources && msg.sources.length > 0) {
            var srcId = 'sources-' + msg.timestamp;
            html += '<div class="chat-sources">';
            html += '<button class="chat-sources-toggle" onclick="toggleSources(\'' + srcId + '\', this)">';
            html += '<span class="arrow">▶</span> 引用来源（' + msg.sources.length + '）';
            html += '</button>';
            html += '<ul id="' + srcId + '" class="chat-sources-list">';
            for (var j = 0; j < msg.sources.length; j++) {
                var src = msg.sources[j];
                html += '<li class="chat-source-item">';
                html += '<span class="chat-source-name">' + escapeHtml(src.document_name || '未知文档') + '</span>';
                if (src.snippet) {
                    html += '<span class="chat-source-snippet">' + escapeHtml(src.snippet) + '</span>';
                }
                if (src.image_url) {
                    html += '<span class="chat-source-snippet">📷 图片来源</span>';
                }
                html += '</li>';
            }
            html += '</ul></div>';
        }

        html += '<span class="chat-msg-time">' + timeStr + '</span>';
        html += '</div>';
        return html;
    }

    function renderLoadingIndicator() {
        return '<div class="chat-msg chat-msg-system chat-msg-loading">' +
            '<div class="chat-msg-bubble">' +
                '<span class="typing-dot"></span>' +
                '<span class="typing-dot"></span>' +
                '<span class="typing-dot"></span>' +
            '</div>' +
        '</div>';
    }

    function scrollChatToBottom() {
        var container = document.getElementById('chat-messages');
        if (container) {
            container.scrollTop = container.scrollHeight;
        }
    }

    function formatTime(ts) {
        if (!ts) return '';
        var d = new Date(ts);
        var h = d.getHours().toString().padStart(2, '0');
        var m = d.getMinutes().toString().padStart(2, '0');
        return h + ':' + m;
    }

    function escapeHtml(str) {
        if (!str) return '';
        return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;').replace(/'/g, '&#039;');
    }

    window.toggleSources = function (id, btn) {
        var list = document.getElementById(id);
        if (!list) return;
        list.classList.toggle('open');
        if (btn) btn.classList.toggle('open');
    };

    window.sendChatMessage = function () {
        var input = document.getElementById('chat-input');
        var sendBtn = document.getElementById('chat-send-btn');
        if (!input) return;

        var question = input.value.trim();
        var imageData = chatPendingImage;
        if ((!question && !imageData) || chatLoading) return;

        // Default question text if only image
        if (!question && imageData) {
            question = '请识别这张图片的内容';
        }

        // Add user message
        var userMsg = {
            role: 'user',
            content: question,
            timestamp: Date.now()
        };
        if (imageData) {
            userMsg.imageUrl = imageData;
        }
        chatMessages.push(userMsg);

        // Clear input, image, and reset height
        input.value = '';
        input.style.height = 'auto';
        chatPendingImage = null;
        var preview = document.getElementById('chat-image-preview');
        if (preview) preview.classList.add('hidden');
        if (sendBtn) sendBtn.disabled = true;

        // Show loading
        chatLoading = true;
        renderChatMessages();

        // Build request body
        var reqBody = {
            question: question,
            user_id: getChatUserID()
        };
        if (imageData) {
            reqBody.image_data = imageData;
        }

        // Call API
        var token = getChatToken();
        fetch('/api/query', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Authorization': 'Bearer ' + token
            },
            body: JSON.stringify(reqBody)
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '请求失败'); });
            return res.json();
        })
        .then(function (data) {
            var msg = {
                role: 'system',
                content: data.answer || data.message || '暂无回答',
                sources: data.sources || [],
                isPending: !!data.is_pending,
                timestamp: Date.now()
            };
            if (data.is_pending) {
                msg.content = data.message || '该问题已转交人工处理，请稍后查看回复';
            }
            chatMessages.push(msg);
        })
        .catch(function (err) {
            chatMessages.push({
                role: 'system',
                content: '抱歉，请求出错：' + (err.message || '未知错误') + '。请稍后重试。',
                sources: [],
                isPending: false,
                timestamp: Date.now()
            });
        })
        .finally(function () {
            chatLoading = false;
            renderChatMessages();
            if (input) input.focus();
        });
    };

    // ============================================================
    // Admin Panel
    // ============================================================

    var adminCurrentTab = 'documents';
    var adminPendingFilter = '';
    var adminDeleteTargetId = null;
    var adminAnswerTargetId = null;
    var adminToastTimer = null;
    var adminRole = '';  // 'super_admin' or 'editor'

    function getAdminToken() {
        var session = getSession();
        return session ? session.id || session.session_id || '' : '';
    }

    function adminFetch(url, options) {
        options = options || {};
        options.headers = options.headers || {};
        options.headers['Authorization'] = 'Bearer ' + getAdminToken();
        return fetch(url, options);
    }

    function showAdminToast(message, type) {
        type = type || 'info';
        var toast = document.getElementById('admin-toast');
        if (!toast) return;
        toast.textContent = message;
        toast.className = 'toast toast-' + type;
        if (adminToastTimer) clearTimeout(adminToastTimer);
        adminToastTimer = setTimeout(function () {
            toast.classList.add('hidden');
        }, 3000);
    }

    // --- Tab Switching ---

    window.switchAdminTab = function (tab) {
        adminCurrentTab = tab;
        // Update nav
        var items = document.querySelectorAll('.admin-nav-item');
        items.forEach(function (item) {
            item.classList.toggle('active', item.getAttribute('data-tab') === tab);
        });
        // Update content
        var tabs = document.querySelectorAll('.admin-tab');
        tabs.forEach(function (t) { t.classList.add('hidden'); });
        var target = document.getElementById('admin-tab-' + tab);
        if (target) target.classList.remove('hidden');
        // Auto-refresh data on tab switch
        if (tab === 'documents') loadDocumentList();
        if (tab === 'pending') loadPendingQuestions();
        if (tab === 'settings') loadAdminSettings();
        if (tab === 'users') loadAdminUsers();
    };

    function initAdmin() {
        setupDropZone();
        initKnowledgeImageZone();
        // Fetch role and apply visibility
        adminFetch('/api/admin/role')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                adminRole = data.role || '';
                applyAdminRoleVisibility();
            })
            .catch(function () {
                adminRole = localStorage.getItem('admin_role') || '';
                applyAdminRoleVisibility();
            })
            .finally(function () {
                switchAdminTab('documents');
            });
    }

    function applyAdminRoleVisibility() {
        // Hide settings and users tabs for non-super_admin
        var settingsNav = document.querySelector('.admin-nav-item[data-tab="settings"]');
        var usersNav = document.querySelector('.admin-nav-item[data-tab="users"]');
        if (adminRole !== 'super_admin') {
            if (settingsNav) settingsNav.style.display = 'none';
            if (usersNav) usersNav.style.display = 'none';
        } else {
            if (settingsNav) settingsNav.style.display = '';
            if (usersNav) usersNav.style.display = '';
        }
    }

    // --- Document Management ---

    function setupDropZone() {
        var zone = document.getElementById('admin-drop-zone');
        if (!zone) return;

        zone.addEventListener('dragover', function (e) {
            e.preventDefault();
            zone.classList.add('dragover');
        });
        zone.addEventListener('dragleave', function () {
            zone.classList.remove('dragover');
        });
        zone.addEventListener('drop', function (e) {
            e.preventDefault();
            zone.classList.remove('dragover');
            var files = e.dataTransfer.files;
            if (files.length > 0) uploadFile(files[0]);
        });
    }

    window.handleAdminFileUpload = function (input) {
        if (input.files && input.files.length > 0) {
            uploadFile(input.files[0]);
            input.value = '';
        }
    };

    function uploadFile(file) {
        var formData = new FormData();
        formData.append('file', file);

        showAdminToast('正在上传 ' + file.name + '...', 'info');

        adminFetch('/api/documents/upload', {
            method: 'POST',
            body: formData
        })
        .then(function (res) {
            if (!res.ok) throw new Error('上传失败');
            return res.json();
        })
        .then(function () {
            showAdminToast('文件上传成功', 'success');
            loadDocumentList();
        })
        .catch(function (err) {
            showAdminToast(err.message || '上传失败', 'error');
        });
    }

    window.handleAdminURLSubmit = function () {
        var input = document.getElementById('admin-url-field');
        if (!input) return;
        var url = input.value.trim();
        if (!url) {
            showAdminToast('请输入URL地址', 'error');
            return;
        }

        showAdminToast('正在提交URL...', 'info');

        adminFetch('/api/documents/url', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ url: url })
        })
        .then(function (res) {
            if (!res.ok) throw new Error('提交失败');
            return res.json();
        })
        .then(function () {
            showAdminToast('URL提交成功', 'success');
            input.value = '';
            loadDocumentList();
        })
        .catch(function (err) {
            showAdminToast(err.message || '提交失败', 'error');
        });
    };

    function loadDocumentList() {
        adminFetch('/api/documents')
            .then(function (res) {
                if (!res.ok) throw new Error('加载失败');
                return res.json();
            })
            .then(function (data) {
                renderDocumentList(data.documents || data || []);
            })
            .catch(function () {
                renderDocumentList([]);
            });
    }

    function renderDocumentList(docs) {
        var tbody = document.getElementById('admin-doc-tbody');
        if (!tbody) return;

        if (!docs || docs.length === 0) {
            tbody.innerHTML = '<tr><td colspan="5" class="admin-table-empty">暂无文档</td></tr>';
            return;
        }

        var html = '';
        for (var i = 0; i < docs.length; i++) {
            var doc = docs[i];
            var statusClass = 'admin-badge-' + (doc.status || 'processing');
            var statusText = { processing: '处理中', success: '成功', failed: '失败' }[doc.status] || doc.status;
            var timeStr = doc.created_at ? new Date(doc.created_at).toLocaleString('zh-CN') : '-';

            var nameCell = '';
            if (doc.type === 'url') {
                nameCell = '<a href="' + escapeHtml(doc.name) + '" target="_blank">' + escapeHtml(doc.name || '-') + '</a>';
            } else if (doc.type === 'answer') {
                nameCell = escapeHtml(doc.name || '-');
            } else {
                nameCell = '<a href="/api/documents/' + escapeHtml(doc.id) + '/download" target="_blank">' + escapeHtml(doc.name || '-') + '</a>';
            }

            html += '<tr>' +
                '<td>' + nameCell + '</td>' +
                '<td>' + escapeHtml(doc.type || '-') + '</td>' +
                '<td><span class="admin-badge ' + statusClass + '">' + escapeHtml(statusText) + '</span></td>' +
                '<td>' + escapeHtml(timeStr) + '</td>' +
                '<td><button class="btn-danger btn-sm" onclick="showDeleteDialog(\'' + escapeHtml(doc.id) + '\', \'' + escapeHtml(doc.name || '') + '\')">删除</button></td>' +
            '</tr>';
        }
        tbody.innerHTML = html;
    }

    // --- Delete Document ---

    window.showDeleteDialog = function (docId, docName) {
        adminDeleteTargetId = docId;
        var msg = document.getElementById('admin-confirm-msg');
        if (msg) msg.textContent = '确定要删除文档"' + docName + '"吗？此操作不可撤销。';
        var dialog = document.getElementById('admin-confirm-dialog');
        if (dialog) dialog.classList.remove('hidden');
    };

    window.closeAdminDialog = function () {
        adminDeleteTargetId = null;
        var dialog = document.getElementById('admin-confirm-dialog');
        if (dialog) dialog.classList.add('hidden');
    };

    window.confirmAdminDelete = function () {
        if (!adminDeleteTargetId) return;
        var docId = adminDeleteTargetId;
        closeAdminDialog();

        adminFetch('/api/documents/' + encodeURIComponent(docId), {
            method: 'DELETE'
        })
        .then(function (res) {
            if (!res.ok) throw new Error('删除失败');
            showAdminToast('文档已删除', 'success');
            loadDocumentList();
        })
        .catch(function (err) {
            showAdminToast(err.message || '删除失败', 'error');
        });
    };

    // --- Pending Questions ---

    window.filterPendingQuestions = function (status) {
        adminPendingFilter = status;
        var btns = document.querySelectorAll('.admin-filter-btn');
        btns.forEach(function (btn) {
            btn.classList.toggle('active', btn.getAttribute('data-status') === status);
        });
        loadPendingQuestions();
    };

    function loadPendingQuestions() {
        var url = '/api/pending';
        if (adminPendingFilter) url += '?status=' + encodeURIComponent(adminPendingFilter);

        adminFetch(url)
            .then(function (res) {
                if (!res.ok) throw new Error('加载失败');
                return res.json();
            })
            .then(function (data) {
                renderPendingQuestions(data.questions || data || []);
            })
            .catch(function () {
                renderPendingQuestions([]);
            });
    }

    function renderPendingQuestions(questions) {
        var container = document.getElementById('admin-pending-list');
        if (!container) return;

        if (!questions || questions.length === 0) {
            container.innerHTML = '<div class="admin-table-empty">暂无问题</div>';
            return;
        }

        var html = '';
        for (var i = 0; i < questions.length; i++) {
            var q = questions[i];
            var statusClass = 'admin-badge-' + (q.status || 'pending');
            var statusText = q.status === 'answered' ? '已回答' : '待回答';
            var timeStr = q.created_at ? new Date(q.created_at).toLocaleString('zh-CN') : '-';

            html += '<div class="admin-pending-card">';
            html += '<div class="admin-pending-card-header">';
            html += '<div class="admin-pending-meta">';
            html += '<span>用户: ' + escapeHtml(q.user_id || '-') + '</span>';
            html += '<span>' + escapeHtml(timeStr) + '</span>';
            html += '</div>';
            html += '<span class="admin-badge ' + statusClass + '">' + escapeHtml(statusText) + '</span>';
            html += '</div>';
            html += '<div class="admin-pending-question">' + escapeHtml(q.question || '') + '</div>';

            if (q.answer) {
                html += '<div class="admin-pending-answer-preview">回答: ' + escapeHtml(q.answer) + '</div>';
            }

            if (q.status !== 'answered') {
                html += '<button class="btn-primary btn-sm admin-answer-btn" data-id="' + escapeHtml(q.id) + '" data-question="' + escapeHtml(q.question || '') + '">回答</button>';
            }

            html += '</div>';
        }
        container.innerHTML = html;

        // Bind answer button clicks
        var answerBtns = container.querySelectorAll('.admin-answer-btn');
        for (var j = 0; j < answerBtns.length; j++) {
            (function(btn) {
                btn.addEventListener('click', function() {
                    showAnswerDialog(btn.getAttribute('data-id'), btn.getAttribute('data-question'));
                });
            })(answerBtns[j]);
        }
    }

    // --- Answer Dialog ---

    window.showAnswerDialog = function (questionId, questionText) {
        adminAnswerTargetId = questionId;
        var textEl = document.getElementById('admin-answer-question-text');
        if (textEl) textEl.textContent = questionText;
        var answerInput = document.getElementById('admin-answer-text');
        if (answerInput) answerInput.value = '';
        var urlInput = document.getElementById('admin-answer-url');
        if (urlInput) urlInput.value = '';
        var dialog = document.getElementById('admin-answer-dialog');
        if (dialog) dialog.classList.remove('hidden');
    };

    window.closeAnswerDialog = function () {
        adminAnswerTargetId = null;
        var dialog = document.getElementById('admin-answer-dialog');
        if (dialog) dialog.classList.add('hidden');
    };

    window.submitAdminAnswer = function () {
        if (!adminAnswerTargetId) return;

        var text = (document.getElementById('admin-answer-text') || {}).value || '';
        var url = (document.getElementById('admin-answer-url') || {}).value || '';

        if (!text.trim() && !url.trim()) {
            showAdminToast('请输入回答内容', 'error');
            return;
        }

        var submitBtn = document.getElementById('admin-answer-submit-btn');
        if (submitBtn) submitBtn.disabled = true;

        adminFetch('/api/pending/answer', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                question_id: adminAnswerTargetId,
                text: text.trim(),
                url: url.trim()
            })
        })
        .then(function (res) {
            if (!res.ok) throw new Error('提交失败');
            showAdminToast('回答已提交', 'success');
            closeAnswerDialog();
            loadPendingQuestions();
        })
        .catch(function (err) {
            showAdminToast(err.message || '提交失败', 'error');
        })
        .finally(function () {
            if (submitBtn) submitBtn.disabled = false;
        });
    };

    // --- Settings ---

    function loadAdminSettings() {
        adminFetch('/api/config')
            .then(function (res) {
                if (!res.ok) throw new Error('加载失败');
                return res.json();
            })
            .then(function (cfg) {
                var server = cfg.server || {};
                var llm = cfg.llm || {};
                var emb = cfg.embedding || {};
                var vec = cfg.vector || {};
                var admin = cfg.admin || {};

                setVal('cfg-server-port', server.port);

                setVal('cfg-llm-endpoint', llm.endpoint);
                setVal('cfg-llm-model', llm.model_name);
                setVal('cfg-llm-apikey', '');
                setPlaceholder('cfg-llm-apikey', llm.api_key ? '***' : '未设置');
                setVal('cfg-llm-temperature', llm.temperature);
                setVal('cfg-llm-maxtokens', llm.max_tokens);

                setVal('cfg-emb-endpoint', emb.endpoint);
                setVal('cfg-emb-model', emb.model_name);
                setVal('cfg-emb-apikey', '');
                setPlaceholder('cfg-emb-apikey', emb.api_key ? '***' : '未设置');
                var mmSelect = document.getElementById('cfg-emb-multimodal');
                if (mmSelect) mmSelect.value = emb.use_multimodal ? 'true' : 'false';

                setVal('cfg-vec-chunksize', vec.chunk_size);
                setVal('cfg-vec-overlap', vec.overlap);
                setVal('cfg-vec-topk', vec.top_k);
                setVal('cfg-vec-threshold', vec.threshold);

                setVal('cfg-admin-login-route', admin.login_route || '/admin');

                setVal('cfg-product-intro', cfg.product_intro || '');

                var smtp = cfg.smtp || {};
                setVal('cfg-smtp-host', smtp.host);
                setVal('cfg-smtp-port', smtp.port);
                setVal('cfg-smtp-username', smtp.username);
                setVal('cfg-smtp-password', '');
                setPlaceholder('cfg-smtp-password', smtp.password ? '***' : '未设置');
                setVal('cfg-smtp-from-addr', smtp.from_addr);
                setVal('cfg-smtp-from-name', smtp.from_name);
                var tlsSelect = document.getElementById('cfg-smtp-tls');
                if (tlsSelect) tlsSelect.value = smtp.use_tls === false ? 'false' : 'true';
            })
            .catch(function () {
                showAdminToast('加载配置失败', 'error');
            });
    }

    function setVal(id, val) {
        var el = document.getElementById(id);
        if (el && val !== undefined && val !== null && val !== '') el.value = val;
    }

    function setPlaceholder(id, val) {
        var el = document.getElementById(id);
        if (el) el.placeholder = val || '';
    }

    function getVal(id) {
        var el = document.getElementById(id);
        return el ? el.value : '';
    }

    window.restartServer = function () {
        if (!confirm('确定要重启服务吗？重启期间服务将短暂不可用。')) return;
        var btn = document.getElementById('server-restart-btn');
        if (btn) btn.disabled = true;

        adminFetch('/api/server/restart', { method: 'POST' })
            .then(function (res) {
                if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '重启失败'); });
                showAdminToast('服务正在重启，请稍候刷新页面...', 'success');
                setTimeout(function () { location.reload(); }, 3000);
            })
            .catch(function (err) {
                showAdminToast(err.message || '重启失败', 'error');
                if (btn) btn.disabled = false;
            });
    };

    window.testSmtpEmail = function () {
        var emailInput = document.getElementById('cfg-smtp-test-email');
        var resultEl = document.getElementById('smtp-test-result');
        var btn = document.getElementById('smtp-test-btn');
        if (!emailInput) return;

        var email = emailInput.value.trim();
        if (!email) {
            if (resultEl) { resultEl.textContent = '请输入收件人邮箱'; resultEl.className = 'error-text'; resultEl.classList.remove('hidden'); }
            return;
        }

        if (btn) btn.disabled = true;
        if (resultEl) { resultEl.textContent = '正在发送...'; resultEl.className = ''; resultEl.classList.remove('hidden'); }

        adminFetch('/api/email/test', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: email })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '发送失败'); });
            return res.json();
        })
        .then(function () {
            if (resultEl) { resultEl.textContent = '测试邮件已发送，请检查收件箱'; resultEl.className = 'success-text'; }
        })
        .catch(function (err) {
            if (resultEl) { resultEl.textContent = err.message; resultEl.className = 'error-text'; }
        })
        .finally(function () {
            if (btn) btn.disabled = false;
        });
    };

    window.saveAdminSettings = function () {
        var updates = {};

        var serverPort = getVal('cfg-server-port');

        var llmEndpoint = getVal('cfg-llm-endpoint');
        var llmModel = getVal('cfg-llm-model');
        var llmApiKey = getVal('cfg-llm-apikey');
        var llmTemp = getVal('cfg-llm-temperature');
        var llmMaxTokens = getVal('cfg-llm-maxtokens');

        var embEndpoint = getVal('cfg-emb-endpoint');
        var embModel = getVal('cfg-emb-model');
        var embApiKey = getVal('cfg-emb-apikey');

        var vecChunkSize = getVal('cfg-vec-chunksize');
        var vecOverlap = getVal('cfg-vec-overlap');
        var vecTopK = getVal('cfg-vec-topk');
        var vecThreshold = getVal('cfg-vec-threshold');

        if (llmEndpoint) updates['llm.endpoint'] = llmEndpoint;
        if (serverPort !== '') updates['server.port'] = parseInt(serverPort, 10);
        if (llmModel) updates['llm.model_name'] = llmModel;
        if (llmApiKey) updates['llm.api_key'] = llmApiKey;
        if (llmTemp !== '') updates['llm.temperature'] = parseFloat(llmTemp);
        if (llmMaxTokens !== '') updates['llm.max_tokens'] = parseInt(llmMaxTokens, 10);

        if (embEndpoint) updates['embedding.endpoint'] = embEndpoint;
        if (embModel) updates['embedding.model_name'] = embModel;
        if (embApiKey) updates['embedding.api_key'] = embApiKey;
        var embMultimodal = getVal('cfg-emb-multimodal');
        updates['embedding.use_multimodal'] = embMultimodal === 'true';

        if (vecChunkSize !== '') updates['vector.chunk_size'] = parseInt(vecChunkSize, 10);
        if (vecOverlap !== '') updates['vector.overlap'] = parseInt(vecOverlap, 10);
        if (vecTopK !== '') updates['vector.top_k'] = parseInt(vecTopK, 10);
        if (vecThreshold !== '') updates['vector.threshold'] = parseFloat(vecThreshold);

        var adminLoginRouteVal = getVal('cfg-admin-login-route');
        if (adminLoginRouteVal) {
            updates['admin.login_route'] = adminLoginRouteVal;
        }

        var productIntro = getVal('cfg-product-intro');
        updates['product_intro'] = productIntro;

        var smtpHost = getVal('cfg-smtp-host');
        var smtpPort = getVal('cfg-smtp-port');
        var smtpUsername = getVal('cfg-smtp-username');
        var smtpPassword = getVal('cfg-smtp-password');
        var smtpFromAddr = getVal('cfg-smtp-from-addr');
        var smtpFromName = getVal('cfg-smtp-from-name');
        var smtpTls = getVal('cfg-smtp-tls');

        if (smtpHost) updates['smtp.host'] = smtpHost;
        if (smtpPort !== '') updates['smtp.port'] = parseInt(smtpPort, 10);
        if (smtpUsername) updates['smtp.username'] = smtpUsername;
        if (smtpPassword) updates['smtp.password'] = smtpPassword;
        if (smtpFromAddr) updates['smtp.from_addr'] = smtpFromAddr;
        if (smtpFromName) updates['smtp.from_name'] = smtpFromName;
        updates['smtp.use_tls'] = smtpTls === 'true';

        if (Object.keys(updates).length === 0) {
            showAdminToast('没有需要保存的更改', 'info');
            return;
        }

        adminFetch('/api/config', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(updates)
        })
        .then(function (res) {
            if (!res.ok) throw new Error('保存失败');
            showAdminToast('设置已保存', 'success');
            loadAdminSettings();
        })
        .catch(function (err) {
            showAdminToast(err.message || '保存失败', 'error');
        });
    };

    // --- Knowledge Entry ---

    var knowledgeImageURLs = [];

    function initKnowledgeImageZone() {
        var area = document.getElementById('knowledge-image-upload-area');
        var input = document.getElementById('knowledge-image-input');
        if (!area || !input) return;

        // Click to select files
        area.addEventListener('click', function () {
            input.click();
        });

        // File input change
        input.addEventListener('change', function () {
            if (input.files && input.files.length > 0) {
                for (var i = 0; i < input.files.length; i++) {
                    uploadKnowledgeImage(input.files[i]);
                }
                input.value = '';
            }
        });

        // Drag and drop
        area.addEventListener('dragover', function (e) {
            e.preventDefault();
            area.classList.add('dragover');
        });
        area.addEventListener('dragleave', function () {
            area.classList.remove('dragover');
        });
        area.addEventListener('drop', function (e) {
            e.preventDefault();
            area.classList.remove('dragover');
            var files = e.dataTransfer.files;
            for (var i = 0; i < files.length; i++) {
                if (files[i].type.indexOf('image/') === 0) {
                    uploadKnowledgeImage(files[i]);
                }
            }
        });

        // Clipboard paste - listen on the whole knowledge tab
        var knowledgeTab = document.getElementById('admin-tab-knowledge');
        if (knowledgeTab) {
            knowledgeTab.addEventListener('paste', function (e) {
                var items = (e.clipboardData || e.originalEvent.clipboardData || {}).items;
                if (!items) return;
                for (var i = 0; i < items.length; i++) {
                    if (items[i].type.indexOf('image/') === 0) {
                        e.preventDefault();
                        var blob = items[i].getAsFile();
                        if (blob) uploadKnowledgeImage(blob);
                    }
                }
            });
        }
    }

    function uploadKnowledgeImage(file) {
        if (file.type.indexOf('image/') !== 0) {
            showAdminToast('请选择图片文件', 'error');
            return;
        }
        if (file.size > 10 * 1024 * 1024) {
            showAdminToast('图片大小不能超过10MB', 'error');
            return;
        }

        // Create preview placeholder
        var preview = document.getElementById('knowledge-image-preview');
        var item = document.createElement('div');
        item.className = 'knowledge-image-item uploading';
        var img = document.createElement('img');
        img.src = URL.createObjectURL(file);
        item.appendChild(img);
        preview.appendChild(item);

        var formData = new FormData();
        formData.append('image', file, file.name || 'paste.png');

        adminFetch('/api/images/upload', {
            method: 'POST',
            body: formData
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '上传失败'); });
            return res.json();
        })
        .then(function (data) {
            item.classList.remove('uploading');
            var idx = knowledgeImageURLs.length;
            knowledgeImageURLs.push(data.url);

            // Add remove button
            var removeBtn = document.createElement('button');
            removeBtn.className = 'knowledge-image-remove';
            removeBtn.textContent = '×';
            removeBtn.setAttribute('aria-label', '删除图片');
            removeBtn.onclick = function () {
                knowledgeImageURLs[idx] = null;
                item.remove();
            };
            item.appendChild(removeBtn);
        })
        .catch(function (err) {
            item.remove();
            showAdminToast(err.message || '图片上传失败', 'error');
        });
    }

    window.submitKnowledgeEntry = function () {
        var title = (document.getElementById('knowledge-title') || {}).value || '';
        var content = (document.getElementById('knowledge-content') || {}).value || '';

        if (!title.trim() || !content.trim()) {
            showAdminToast('请输入标题和内容', 'error');
            return;
        }

        var imageURLs = knowledgeImageURLs.filter(function (u) { return u; });

        var btn = document.getElementById('knowledge-submit-btn');
        if (btn) btn.disabled = true;
        showAdminToast('正在录入知识...', 'info');

        adminFetch('/api/knowledge', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ title: title.trim(), content: content.trim(), image_urls: imageURLs })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '录入失败'); });
            return res.json();
        })
        .then(function () {
            showAdminToast('知识录入成功', 'success');
            if (document.getElementById('knowledge-title')) document.getElementById('knowledge-title').value = '';
            if (document.getElementById('knowledge-content')) document.getElementById('knowledge-content').value = '';
            var preview = document.getElementById('knowledge-image-preview');
            if (preview) preview.innerHTML = '';
            knowledgeImageURLs = [];
        })
        .catch(function (err) {
            showAdminToast(err.message || '录入失败', 'error');
        })
        .finally(function () {
            if (btn) btn.disabled = false;
        });
    };

    // --- Admin User Management ---

    function loadAdminUsers() {
        adminFetch('/api/admin/users')
            .then(function (res) {
                if (!res.ok) throw new Error('加载失败');
                return res.json();
            })
            .then(function (data) {
                renderAdminUsers(data.users || []);
            })
            .catch(function () {
                renderAdminUsers([]);
            });
    }

    function renderAdminUsers(users) {
        var tbody = document.getElementById('admin-users-tbody');
        if (!tbody) return;

        if (!users || users.length === 0) {
            tbody.innerHTML = '<tr><td colspan="4" class="admin-table-empty">暂无子账号</td></tr>';
            return;
        }

        var roleMap = { 'editor': '编辑员', 'super_admin': '超级管理员' };
        var html = '';
        for (var i = 0; i < users.length; i++) {
            var u = users[i];
            html += '<tr>' +
                '<td>' + escapeHtml(u.username) + '</td>' +
                '<td>' + escapeHtml(roleMap[u.role] || u.role) + '</td>' +
                '<td>' + escapeHtml(u.created_at || '-') + '</td>' +
                '<td><button class="btn-danger btn-sm" onclick="deleteAdminUser(\'' + escapeHtml(u.id) + '\', \'' + escapeHtml(u.username) + '\')">删除</button></td>' +
            '</tr>';
        }
        tbody.innerHTML = html;
    }

    window.createAdminUser = function () {
        var username = (document.getElementById('admin-new-username') || {}).value || '';
        var password = (document.getElementById('admin-new-password') || {}).value || '';
        var role = (document.getElementById('admin-new-role') || {}).value || 'editor';

        if (!username.trim() || !password) {
            showAdminToast('请输入用户名和密码', 'error');
            return;
        }
        if (password.length < 6) {
            showAdminToast('密码至少6位', 'error');
            return;
        }

        adminFetch('/api/admin/users', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username.trim(), password: password, role: role })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '创建失败'); });
            return res.json();
        })
        .then(function () {
            showAdminToast('用户创建成功', 'success');
            if (document.getElementById('admin-new-username')) document.getElementById('admin-new-username').value = '';
            if (document.getElementById('admin-new-password')) document.getElementById('admin-new-password').value = '';
            loadAdminUsers();
        })
        .catch(function (err) {
            showAdminToast(err.message || '创建失败', 'error');
        });
    };

    window.deleteAdminUser = function (id, username) {
        if (!confirm('确定要删除用户"' + username + '"吗？')) return;

        adminFetch('/api/admin/users/' + encodeURIComponent(id), {
            method: 'DELETE'
        })
        .then(function (res) {
            if (!res.ok) throw new Error('删除失败');
            showAdminToast('用户已删除', 'success');
            loadAdminUsers();
        })
        .catch(function (err) {
            showAdminToast(err.message || '删除失败', 'error');
        });
    };

    // --- Logout ---

    window.logout = function () {
        chatMessages = [];
        chatLoading = false;
        adminRole = '';
        localStorage.removeItem('admin_role');
        clearSession();
        navigate('/login');
    };

    // --- Init ---

    function init() {
        // Fetch admin login route, then handle routing
        fetch('/api/admin/status')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (data.login_route) adminLoginRoute = data.login_route;
            })
            .catch(function () { /* use default */ })
            .finally(function () {
                handleRoute();
            });
    }

    // Run on DOM ready
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

})();
