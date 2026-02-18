// ============================================================
// SPA Router & App State
// ============================================================

(function () {
    'use strict';

    var SESSION_KEY = 'askflow_session';
    var USER_KEY = 'askflow_user';
    var ADMIN_SESSION_KEY = 'askflow_admin_session';
    var ADMIN_USER_KEY = 'askflow_admin_user';
    var adminLoginRoute = '/admin'; // default, will be fetched from server
    var systemReady = true; // assume ready until checked
    var loginCaptchaId = '';
    var registerCaptchaId = '';
    var adminCaptchaId = '';
    var urlProductName = ''; // product name from URL query string, e.g. ?askflow
    var maxUploadSizeMB = 500; // default, will be fetched from server
    var cachedProducts = null; // shared product list cache to avoid duplicate fetches

    // Parse URL query string for product name: ?productName (bare key, no value)
    (function () {
        var search = window.location.search;
        if (search && search.length > 1) {
            var raw = search.substring(1); // remove leading '?'
            // If it's a bare key (no '=' sign), treat it as product name
            if (raw.indexOf('=') === -1 && raw.indexOf('&') === -1) {
                urlProductName = decodeURIComponent(raw);
            }
        }
    })();

    // Shared product fetch — returns a promise, caches the result
    var _productFetchPromise = null;
    function fetchProducts() {
        if (cachedProducts) {
            return Promise.resolve(cachedProducts);
        }
        if (!_productFetchPromise) {
            _productFetchPromise = fetch('/api/products')
                .then(function (res) { return res.json(); })
                .then(function (data) {
                    cachedProducts = data.products || [];
                    return cachedProducts;
                })
                .catch(function () {
                    _productFetchPromise = null;
                    return [];
                });
        }
        return _productFetchPromise;
    }

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
        var adminSession = getAdminSession();
        var adminUser = getAdminUser();
        var isAdmin = adminSession && adminUser && adminUser.provider === 'admin';

        // OAuth callback is handled in init(), skip routing
        if (route === '/oauth/callback') return;

        // If system is not ready (API keys not configured), show setup page
        // except for admin login/panel routes so admin can configure the system
        if (!systemReady) {
            if (route === adminLoginRoute || route === '/admin-panel') {
                // Allow admin to access admin pages to configure the system
            } else {
                showPage('setup');
                return;
            }
        }

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

    // --- Admin Session Management (isolated from user session) ---

    function getAdminSession() {
        try {
            var data = localStorage.getItem(ADMIN_SESSION_KEY);
            if (!data) return null;
            var session = JSON.parse(data);
            if (session.expires_at && new Date(session.expires_at) < new Date()) {
                clearAdminSession();
                return null;
            }
            return session;
        } catch (e) {
            return null;
        }
    }

    function saveAdminSession(session, user) {
        localStorage.setItem(ADMIN_SESSION_KEY, JSON.stringify(session));
        if (user) {
            localStorage.setItem(ADMIN_USER_KEY, JSON.stringify(user));
        }
    }

    function clearAdminSession() {
        localStorage.removeItem(ADMIN_SESSION_KEY);
        localStorage.removeItem(ADMIN_USER_KEY);
    }

    function getAdminUser() {
        try {
            var data = localStorage.getItem(ADMIN_USER_KEY);
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
                if (el) el.textContent = i18n.t('captcha_load_fail');
            });
    }

    window.loadLoginCaptcha = function () {
        loadCaptcha('user-login-captcha-question', 'login');
    };

    window.loadRegisterCaptcha = function () {
        loadCaptcha('user-register-captcha-question', 'register');
    };

    window.loadAdminCaptcha = function () {
        fetch('/api/captcha/image')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                var img = document.getElementById('admin-login-captcha-img');
                if (img) img.src = data.image;
                adminCaptchaId = data.id;
            })
            .catch(function () {
                var img = document.getElementById('admin-login-captcha-img');
                if (img) img.alt = i18n.t('captcha_load_fail');
            });
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
            if (errorEl) { errorEl.textContent = i18n.t('login_error_email_password'); errorEl.classList.remove('hidden'); }
            return;
        }
        if (!captchaInput || !captchaInput.value.trim()) {
            if (errorEl) { errorEl.textContent = i18n.t('login_error_captcha'); errorEl.classList.remove('hidden'); }
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
            if (!res.ok) {
                return res.text().then(function (text) {
                    var msg = i18n.t('login_failed');
                    try { var d = JSON.parse(text); if (d.error) msg = d.error; } catch (e) { /* non-JSON response (e.g. 504 from nginx) */ }
                    throw new Error(msg);
                });
            }
            return res.json();
        })
        .then(function (data) {
            if (data.session) {
                saveSession(data.session, data.user);
                // Product selection is handled in chat page, just set user's default if available
                var defaultPid = (data.user && data.user.default_product_id) ? data.user.default_product_id : '';
                if (defaultPid) {
                    localStorage.setItem('askflow_product_id', defaultPid);
                }
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

        if (!email) { if (errorEl) { errorEl.textContent = i18n.t('register_error_email'); errorEl.classList.remove('hidden'); } return; }
        if (!password) { if (errorEl) { errorEl.textContent = i18n.t('register_error_password'); errorEl.classList.remove('hidden'); } return; }
        if (password.length < 8) { if (errorEl) { errorEl.textContent = i18n.t('register_error_password_length'); errorEl.classList.remove('hidden'); } return; }
        if (password !== confirm) { if (errorEl) { errorEl.textContent = i18n.t('register_error_password_mismatch'); errorEl.classList.remove('hidden'); } return; }
        if (!captchaInput || !captchaInput.value.trim()) { if (errorEl) { errorEl.textContent = i18n.t('register_error_captcha'); errorEl.classList.remove('hidden'); } return; }

        if (errorEl) errorEl.classList.add('hidden');
        if (successEl) successEl.classList.add('hidden');
        if (submitBtn) submitBtn.disabled = true;

        fetch('/api/auth/register', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: email, name: name, password: password, captcha_id: registerCaptchaId, captcha_answer: captchaAnswer })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('register_failed')); });
            return res.json();
        })
        .then(function (data) {
            if (successEl) { successEl.textContent = data.message || i18n.t('register_success'); successEl.classList.remove('hidden'); }
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
            if (statusEl) statusEl.innerHTML = '<p class="error-text">' + i18n.t('verify_invalid_link') + '</p>';
            return;
        }

        fetch('/api/auth/verify?token=' + encodeURIComponent(token))
            .then(function (res) {
                if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('verify_failed')); });
                return res.json();
            })
            .then(function (data) {
                if (statusEl) {
                    statusEl.innerHTML = '<p class="success-text">' + escapeHtml(data.message || i18n.t('verify_success')) + '</p>' +
                        '<p style="margin-top:1rem;"><a href="/login">' + i18n.t('verify_go_login') + '</a></p>';
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
                    loadAdminCaptcha();
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
                loadAdminCaptcha();
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
            if (errorEl) { errorEl.textContent = i18n.t('admin_error_username'); errorEl.classList.remove('hidden'); }
            return;
        }
        if (!password) {
            if (errorEl) { errorEl.textContent = i18n.t('admin_error_password'); errorEl.classList.remove('hidden'); }
            return;
        }
        if (password.length < 6) {
            if (errorEl) { errorEl.textContent = i18n.t('admin_error_password_length'); errorEl.classList.remove('hidden'); }
            return;
        }
        if (password !== confirm) {
            if (errorEl) { errorEl.textContent = i18n.t('admin_error_password_mismatch'); errorEl.classList.remove('hidden'); }
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
                if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_setup_failed')); });
                return res.json();
            })
            .then(function (data) {
                if (data.session) {
                    saveAdminSession(data.session, { username: username, provider: 'admin' });
                    if (data.role) localStorage.setItem('admin_role', data.role);
                    navigate('/admin-panel');
                } else {
                    throw new Error(i18n.t('admin_setup_failed'));
                }
            })
            .catch(function (err) {
                if (errorEl) { errorEl.textContent = err.message || i18n.t('admin_setup_failed'); errorEl.classList.remove('hidden'); }
            })
            .finally(function () {
                if (submitBtn) submitBtn.disabled = false;
            });
    };

    window.handleAdminLogin = function () {
        var usernameInput = document.getElementById('admin-username');
        var input = document.getElementById('admin-password');
        var captchaInput = document.getElementById('admin-login-captcha');
        var errorEl = document.getElementById('admin-login-error');
        var submitBtn = document.querySelector('#admin-login-form .admin-submit-btn');

        if (!usernameInput || !input) return;

        var username = usernameInput.value.trim();
        var password = input.value.trim();
        var captchaAnswer = captchaInput ? captchaInput.value.trim() : '';

        if (!username || !password) {
            if (errorEl) {
                errorEl.textContent = i18n.t('admin_error_credentials');
                errorEl.classList.remove('hidden');
            }
            return;
        }
        if (!captchaAnswer) {
            if (errorEl) {
                errorEl.textContent = i18n.t('admin_error_captcha');
                errorEl.classList.remove('hidden');
            }
            return;
        }

        if (errorEl) errorEl.classList.add('hidden');
        if (submitBtn) submitBtn.disabled = true;

        fetch('/api/admin/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username, password: password, captcha_id: adminCaptchaId, captcha_answer: captchaAnswer })
        })
            .then(function (res) {
                if (!res.ok) {
                    return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_login_failed')); });
                }
                return res.json();
            })
            .then(function (data) {
                if (data.session) {
                    saveAdminSession(data.session, { username: username, provider: 'admin' });
                    if (data.role) localStorage.setItem('admin_role', data.role);
                    navigate('/admin-panel');
                } else {
                    throw new Error(i18n.t('admin_login_failed'));
                }
            })
            .catch(function (err) {
                if (errorEl) {
                    errorEl.textContent = err.message || i18n.t('admin_login_retry');
                    errorEl.classList.remove('hidden');
                }
                input.value = '';
                if (captchaInput) captchaInput.value = '';
                loadAdminCaptcha();
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
            if (el && (el.id === 'admin-username' || el.id === 'admin-password' || el.id === 'admin-login-captcha')) {
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
        var userMenu = document.getElementById('chat-user-menu');
        var session = getSession();

        if (nameEl) {
            try {
                var user = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
                nameEl.textContent = user.name || user.email || '';
            } catch (e) { /* ignore */ }
        }
        if (session) {
            if (loginBtn) loginBtn.classList.add('hidden');
            if (logoutBtn) logoutBtn.classList.add('hidden');
            if (userMenu) userMenu.classList.remove('hidden');
        } else {
            if (loginBtn) loginBtn.classList.remove('hidden');
            if (logoutBtn) logoutBtn.classList.add('hidden');
            if (userMenu) userMenu.classList.add('hidden');
        }

        // Load products and resolve initial product in one flow (shared cache)
        loadChatProducts();
        resolveInitialProduct(function () {
            if (chatMessages.length === 0) {
                loadWelcomeMessage();
            } else {
                renderChatMessages();
            }
        });

        setupChatInput();

        // Close dropdown when clicking outside
        document.addEventListener('click', function (e) {
            var dropdown = document.getElementById('chat-user-dropdown');
            var menu = document.getElementById('chat-user-menu');
            if (dropdown && menu && !menu.contains(e.target)) {
                dropdown.classList.add('hidden');
            }
        });
    }

    // Load products into the chat header product selector
    function loadChatProducts() {
        var selector = document.getElementById('chat-product-selector');
        var defaultSelect = document.getElementById('chat-default-product');
        if (!selector) return;

        fetchProducts()
            .then(function (products) {
                if (products.length === 0) {
                    selector.classList.add('hidden');
                    return;
                }
                // Populate chat header selector (no "all products" option)
                selector.innerHTML = '';
                var defaultHTML = '<option value="" data-i18n="chat_no_default_product">' + i18n.t('chat_no_default_product') + '</option>';
                // Filter out "all products" entries
                var filtered = products.filter(function (p) {
                    var n = (p.name || '').trim().toLowerCase();
                    return n !== '全部产品' && n !== 'all products';
                });
                if (filtered.length === 0) {
                    selector.classList.add('hidden');
                    return;
                }
                for (var i = 0; i < filtered.length; i++) {
                    var opt = '<option value="' + filtered[i].id + '">' + escapeHtml(filtered[i].name) + '</option>';
                    selector.innerHTML += opt;
                    defaultHTML += opt;
                }
                if (defaultSelect) defaultSelect.innerHTML = defaultHTML;

                // Set current product, default to first product if none selected
                var currentPid = localStorage.getItem('askflow_product_id') || '';
                if (currentPid) {
                    selector.value = currentPid;
                } else if (filtered.length > 0) {
                    // Default to first product
                    selector.value = filtered[0].id;
                    localStorage.setItem('askflow_product_id', filtered[0].id);
                    localStorage.setItem('askflow_product_name', filtered[0].name);
                }

                // Set default product in dropdown
                var session = getSession();
                if (session && defaultSelect) {
                    var token = session.id || session.session_id || '';
                    fetch('/api/user/preferences', {
                        headers: { 'Authorization': 'Bearer ' + token }
                    })
                    .then(function (res) { return res.json(); })
                    .then(function (pref) {
                        if (pref.default_product_id && defaultSelect) {
                            defaultSelect.value = pref.default_product_id;
                        }
                    })
                    .catch(function () { /* ignore */ });
                }

                selector.classList.remove('hidden');

                // Listen for product switch (bind only once)
                if (!selector._changeListenerBound) {
                    selector._changeListenerBound = true;
                    selector.addEventListener('change', function () {
                        var newPid = this.value;
                        if (newPid) {
                            localStorage.setItem('askflow_product_id', newPid);
                            var selOpt = this.options[this.selectedIndex];
                            if (selOpt) localStorage.setItem('askflow_product_name', selOpt.textContent);
                        } else {
                            localStorage.removeItem('askflow_product_id');
                            localStorage.removeItem('askflow_product_name');
                        }
                        // Clear chat and load new welcome message
                        chatMessages = [];
                        loadWelcomeMessage();
                    });
                }
            })
            .catch(function () {
                selector.classList.add('hidden');
            });
    }

    // Resolve initial product: URL param > user default > localStorage
    function resolveInitialProduct(callback) {
        // 1. URL parameter takes highest priority
        if (urlProductName) {
            fetchProducts()
                .then(function (products) {
                    var lowerURL = urlProductName.toLowerCase();
                    for (var j = 0; j < products.length; j++) {
                        if (products[j].name.toLowerCase() === lowerURL) {
                            localStorage.setItem('askflow_product_id', products[j].id);
                            localStorage.setItem('askflow_product_name', products[j].name);
                            var selector = document.getElementById('chat-product-selector');
                            if (selector) selector.value = products[j].id;
                            break;
                        }
                    }
                    if (callback) callback();
                })
                .catch(function () { if (callback) callback(); });
            return;
        }

        // 2. If no localStorage product, try user's default
        if (!localStorage.getItem('askflow_product_id')) {
            var session = getSession();
            if (session) {
                var token = session.id || session.session_id || '';
                fetch('/api/user/preferences', {
                    headers: { 'Authorization': 'Bearer ' + token }
                })
                .then(function (res) { return res.json(); })
                .then(function (pref) {
                    if (pref.default_product_id) {
                        localStorage.setItem('askflow_product_id', pref.default_product_id);
                        var selector = document.getElementById('chat-product-selector');
                        if (selector) selector.value = pref.default_product_id;
                    }
                    if (callback) callback();
                })
                .catch(function () { if (callback) callback(); });
                return;
            }
        }

        if (callback) callback();
    }

    // Load welcome message for current product
    function loadWelcomeMessage() {
        var productId = localStorage.getItem('askflow_product_id') || '';
        var introUrl = '/api/product-intro' + (productId ? '?product_id=' + encodeURIComponent(productId) : '');
        fetch(introUrl)
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (data.product_intro) {
                    chatMessages.push({
                        role: 'system',
                        content: data.product_intro,
                        sources: [],
                        isPending: false,
                        isWelcome: true,
                        timestamp: Date.now()
                    });
                }
                renderChatMessages();
            })
            .catch(function () {
                renderChatMessages();
            });
    }

    // Toggle user profile dropdown
    window.toggleUserDropdown = function () {
        var dropdown = document.getElementById('chat-user-dropdown');
        if (dropdown) dropdown.classList.toggle('hidden');
    };

    // Save default product preference
    window.saveDefaultProduct = function () {
        var select = document.getElementById('chat-default-product');
        if (!select) return;
        var session = getSession();
        if (!session) return;
        var token = session.id || session.session_id || '';
        fetch('/api/user/preferences', {
            method: 'PUT',
            headers: {
                'Content-Type': 'application/json',
                'Authorization': 'Bearer ' + token
            },
            body: JSON.stringify({ default_product_id: select.value })
        })
        .then(function (res) {
            if (res.ok) {
                showChatToast(i18n.t('chat_default_product_saved') || '默认产品已保存', 'success');
            }
        })
        .catch(function () { /* ignore */ });
    };

    function showChatToast(message, type) {
        type = type || 'info';
        var toast = document.getElementById('chat-toast');
        if (!toast) return;
        toast.textContent = message;
        toast.className = 'toast toast-' + type;
        setTimeout(function () { toast.classList.add('hidden'); }, 3000);
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

        // Drag-and-drop image support on the input wrapper
        var wrapper = document.getElementById('chat-input-wrapper');
        if (wrapper) {
            wrapper.addEventListener('dragover', function (e) {
                e.preventDefault();
                e.stopPropagation();
                wrapper.classList.add('drag-over');
            });
            wrapper.addEventListener('dragleave', function (e) {
                e.preventDefault();
                e.stopPropagation();
                wrapper.classList.remove('drag-over');
            });
            wrapper.addEventListener('drop', function (e) {
                e.preventDefault();
                e.stopPropagation();
                wrapper.classList.remove('drag-over');
                var files = e.dataTransfer && e.dataTransfer.files;
                if (!files || files.length === 0) return;
                var file = files[0];
                if (file.type.indexOf('image') === -1) return;
                if (file.size > 10 * 1024 * 1024) {
                    alert(i18n.t('image_size_error'));
                    return;
                }
                var reader = new FileReader();
                reader.onload = function (ev) {
                    chatPendingImage = ev.target.result;
                    showChatImagePreview(chatPendingImage);
                    updateSendBtnState();
                };
                reader.readAsDataURL(file);
            });
        }
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

    // Handle image file selection from the upload button
    window.handleChatImageFileSelect = function (input) {
        if (!input.files || input.files.length === 0) return;
        var file = input.files[0];
        if (file.type.indexOf('image') === -1) {
            alert(i18n.t('image_select_error'));
            input.value = '';
            return;
        }
        if (file.size > 10 * 1024 * 1024) {
            alert(i18n.t('image_size_error'));
            input.value = '';
            return;
        }
        var reader = new FileReader();
        reader.onload = function (ev) {
            chatPendingImage = ev.target.result;
            showChatImagePreview(chatPendingImage);
            updateSendBtnState();
        };
        reader.readAsDataURL(file);
        // Reset input so the same file can be selected again
        input.value = '';
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
                    '<h3>' + i18n.t('chat_welcome_title') + '</h3>' +
                    '<p>' + i18n.t('chat_welcome_desc') + '</p>' +
                '</div>';
            return;
        }

        var html = '';
        for (var i = 0; i < chatMessages.length; i++) {
            html += renderSingleMessage(chatMessages[i], i === chatMessages.length - 1, i);
        }
        if (chatLoading) {
            html += renderLoadingIndicator();
        }
        container.innerHTML = html;
        scrollChatToBottom();
    }

    function renderSingleMessage(msg, isLast, i) {
        var timeStr = formatTime(msg.timestamp);

        if (msg.role === 'user') {
            var userHtml = '<div class="chat-msg chat-msg-user">' +
                '<div class="chat-msg-bubble">' + linkifyText(escapeHtml(msg.content));
            if (msg.imageUrl) {
                var showProgress = chatLoading && isLast;
                userHtml += '<div class="chat-msg-user-image">' +
                    '<img src="' + msg.imageUrl + '" alt="' + i18n.t('chat_user_image_alt') + '" />' +
                    (showProgress ? '<div class="img-progress-overlay"><div class="img-progress-bar indeterminate"></div></div>' : '') +
                    '</div>';
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
        html += renderMarkdown(msg.content);

        // Display images and video/audio from sources inline
        if (msg.sources && msg.sources.length > 0) {
            var videoSegments = {};
            for (var k = 0; k < msg.sources.length; k++) {
                var s = msg.sources[k];
                if (s.image_url) {
                    html += '<div class="chat-msg-image"><img src="' + escapeHtml(s.image_url) + '" alt="' + escapeHtml(s.document_name || 'image') + '" loading="lazy" style="max-width:100%;border-radius:8px;margin-top:8px;cursor:pointer;" onclick="window.open(this.src,\'_blank\')" /></div>';
                }
                if (s.document_type === 'video' && s.document_id) {
                    if (!videoSegments[s.document_id]) {
                        videoSegments[s.document_id] = { name: s.document_name, idx: k, times: [] };
                    }
                    if (s.start_time > 0 || s.end_time > 0) {
                        videoSegments[s.document_id].times.push({ start: s.start_time || 0, end: s.end_time || 0 });
                    }
                }
            }
            var docIds = Object.keys(videoSegments);
            for (var vi = 0; vi < docIds.length; vi++) {
                var vDocId = docIds[vi];
                var seg = videoSegments[vDocId];
                var mediaUrl = '/api/media/' + encodeURIComponent(vDocId);
                var firstStart = seg.times.length > 0 ? seg.times[0].start : 0;
                var playerId = 'media-' + msg.timestamp + '-' + seg.idx;
                var vExt = (seg.name || '').split('.').pop().toLowerCase();
                var isAudio = (vExt === 'mp3' || vExt === 'wav' || vExt === 'ogg' || vExt === 'flac');
                html += '<div class="chat-media-player">';
                html += '<div class="chat-media-label">' + (isAudio ? '🎵' : '🎬') + ' ' + escapeHtml(seg.name || 'video') + '</div>';
                if (isAudio) {
                    html += '<audio id="' + playerId + '" controls preload="metadata" class="chat-audio-element"' + (firstStart > 0 ? ' onloadedmetadata="this.currentTime=' + firstStart + '"' : '') + '><source src="' + mediaUrl + '"></audio>';
                } else {
                    html += '<video id="' + playerId + '" controls preload="metadata" class="chat-video-element"' + (firstStart > 0 ? ' onloadedmetadata="this.currentTime=' + firstStart + '"' : '') + '><source src="' + mediaUrl + '"></video>';
                }
                if (seg.times.length > 0) {
                    html += '<div class="chat-media-segments">';
                    for (var ti = 0; ti < seg.times.length; ti++) {
                        var tm = seg.times[ti];
                        var tmLabel = formatMediaTime(tm.start);
                        if (tm.end > 0 && tm.end !== tm.start) tmLabel += ' - ' + formatMediaTime(tm.end);
                        html += '<button class="chat-media-seg-btn" onclick="seekMedia(\'' + playerId + '\',' + tm.start + ')">' + tmLabel + '</button>';
                    }
                    html += '</div>';
                }
                html += '</div>';
            }
        }
        html += '</div>';

        // Sources
        if (msg.sources && msg.sources.length > 0) {
            var srcId = 'sources-' + msg.timestamp;
            var downloadableTypes = { pdf:1, doc:1, docx:1, word:1, xls:1, xlsx:1, excel:1, ppt:1, pptx:1, video:1, mp4:1, avi:1, mkv:1, mov:1, webm:1 };
            var productId = localStorage.getItem('askflow_product_id') || '';
            html += '<div class="chat-sources">';
            html += '<button class="chat-sources-toggle" onclick="toggleSources(\'' + srcId + '\', this)">';
            html += '<span class="arrow">▶</span> ' + i18n.t('chat_source_toggle') + '（' + msg.sources.length + '）';
            html += '</button>';
            html += '<ul id="' + srcId + '" class="chat-sources-list">';
            for (var j = 0; j < msg.sources.length; j++) {
                var src = msg.sources[j];
                html += '<li class="chat-source-item">';
                var docName = escapeHtml(src.document_name || i18n.t('chat_source_unknown'));
                var canDownload = msg.allowDownload && src.document_id && src.document_type && downloadableTypes[(src.document_type || '').toLowerCase()];
                if (canDownload) {
                    var dlToken = getChatToken();
                    html += '<a class="chat-source-name chat-source-download" href="/api/documents/public-download/' + encodeURIComponent(src.document_id) + '?product_id=' + encodeURIComponent(productId) + '&token=' + encodeURIComponent(dlToken) + '" title="' + i18n.t('chat_source_download') + '">📥 ' + docName + '</a>';
                } else {
                    html += '<span class="chat-source-name">' + docName + '</span>';
                }
                if (src.start_time > 0 || src.end_time > 0) {
                    var timeLabel = formatMediaTime(src.start_time || 0);
                    if (src.end_time > 0 && src.end_time !== src.start_time) {
                        timeLabel += ' - ' + formatMediaTime(src.end_time);
                    }
                    html += '<span class="chat-source-time">🕐 ' + timeLabel + '</span>';
                }
                if (src.snippet) {
                    html += '<span class="chat-source-snippet">' + escapeHtml(src.snippet) + '</span>';
                }
                if (src.image_url) {
                    html += '<span class="chat-source-snippet">' + i18n.t('chat_source_image') + '</span>';
                }
                html += '</li>';
            }
            html += '</ul></div>';
        }

        // Debug info (when debug mode is enabled)
        if (msg.debugInfo) {
            var dbgId = 'debug-' + msg.timestamp;
            html += '<div class="chat-sources">';
            html += '<button class="chat-sources-toggle" onclick="toggleSources(\'' + dbgId + '\', this)">';
            html += '<span class="arrow">▶</span> 🔍 ' + i18n.t('chat_debug_toggle');
            html += '</button>';
            html += '<div id="' + dbgId + '" class="chat-sources-list chat-debug-info" style="font-size:12px;font-family:monospace;">';
            var di = msg.debugInfo;
            html += '<div><b>Intent:</b> ' + escapeHtml(di.intent || '-') + '</div>';
            html += '<div><b>Vector Dim:</b> ' + (di.vector_dim || 0) + '</div>';
            html += '<div><b>TopK:</b> ' + (di.top_k || 0) + ' | <b>Threshold:</b> ' + (di.threshold || 0) + '</div>';
            html += '<div><b>Results:</b> ' + (di.result_count || 0) + '</div>';
            if (di.relaxed_search) {
                html += '<div><b>Relaxed Search:</b> Yes</div>';
            }
            if (di.top_results && di.top_results.length > 0) {
                html += '<div><b>Top Hits:</b></div><ul style="margin:2px 0 2px 16px;">';
                for (var ti = 0; ti < di.top_results.length; ti++) {
                    var tr = di.top_results[ti];
                    html += '<li>' + escapeHtml(tr.doc_name) + ' (score=' + tr.score.toFixed(4) + ')</li>';
                }
                html += '</ul>';
            }
            if (di.relaxed_results && di.relaxed_results.length > 0) {
                html += '<div><b>Relaxed Hits:</b></div><ul style="margin:2px 0 2px 16px;">';
                for (var ri = 0; ri < di.relaxed_results.length; ri++) {
                    var rr = di.relaxed_results[ri];
                    html += '<li>' + escapeHtml(rr.doc_name) + ' (score=' + rr.score.toFixed(4) + ')</li>';
                }
                html += '</ul>';
            }
            if (di.llm_unable_answer) {
                html += '<div style="color:#e67e22;"><b>LLM Unable to Answer:</b> Yes</div>';
            }
            if (di.steps && di.steps.length > 0) {
                html += '<div><b>Pipeline Steps:</b></div><ol style="margin:2px 0 2px 16px;">';
                for (var si = 0; si < di.steps.length; si++) {
                    html += '<li>' + escapeHtml(di.steps[si]) + '</li>';
                }
                html += '</ol>';
            }
            html += '</div></div>';
        }

        html += '<span class="chat-msg-time">' + timeStr + '</span>';

        // Add "Not Satisfied" button for non-pending, non-welcome, non-error system answers
        if (!msg.isPending && !msg.isWelcome && !msg.isError && msg.content) {
            html += '<button class="chat-not-satisfied-btn" onclick="window.handleNotSatisfied(this, ' + i + ')">👎 ' + i18n.t('chat_not_satisfied') + '</button>';
        }

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

    // Format seconds into MM:SS or HH:MM:SS for media time display
    function formatMediaTime(seconds) {
        if (!seconds || seconds < 0) return '0:00';
        var s = Math.floor(seconds);
        var h = Math.floor(s / 3600);
        var m = Math.floor((s % 3600) / 60);
        var sec = s % 60;
        if (h > 0) {
            return h + ':' + m.toString().padStart(2, '0') + ':' + sec.toString().padStart(2, '0');
        }
        return m + ':' + sec.toString().padStart(2, '0');
    }

    // Seek a video/audio element to a specific time
    window.seekMedia = function(playerId, seconds) {
        var el = document.getElementById(playerId);
        if (el) {
            el.currentTime = seconds;
            el.play();
        }
    };

    function escapeHtml(str) {
        if (!str) return '';
        return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;').replace(/'/g, '&#039;');
    }

    function linkifyText(str) {
        if (!str) return '';
        return str.replace(/(https?:\/\/[^\s<&]+)/g, '<a href="$1" target="_blank" rel="noopener noreferrer">$1</a>');
    }

    // Lightweight markdown renderer for chat messages
    function renderMarkdown(str) {
        if (!str) return '';
        var text = escapeHtml(str);

        // Code blocks (``` ... ```)
        text = text.replace(/```(\w*)\n([\s\S]*?)```/g, function (m, lang, code) {
            return '<pre class="md-code-block"><code>' + code.replace(/\n$/, '') + '</code></pre>';
        });

        // Inline code
        text = text.replace(/`([^`\n]+)`/g, '<code class="md-code-inline">$1</code>');

        // Headers (###### to #, most specific first)
        text = text.replace(/^###### (.+)$/gm, '<strong>$1</strong>');
        text = text.replace(/^##### (.+)$/gm, '<strong>$1</strong>');
        text = text.replace(/^#### (.+)$/gm, '<strong style="font-size:1.02em;">$1</strong>');
        text = text.replace(/^### (.+)$/gm, '<strong style="font-size:1.05em;">$1</strong>');
        text = text.replace(/^## (.+)$/gm, '<strong style="font-size:1.1em;">$1</strong>');
        text = text.replace(/^# (.+)$/gm, '<strong style="font-size:1.15em;">$1</strong>');

        // Bold **text** or __text__
        text = text.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
        text = text.replace(/__(.+?)__/g, '<strong>$1</strong>');

        // Italic *text* or _text_ (but not inside words with underscores)
        text = text.replace(/\*(.+?)\*/g, '<em>$1</em>');

        // Horizontal rule
        text = text.replace(/^---+$/gm, '<hr style="border:none;border-top:1px solid #e5e7eb;margin:8px 0;">');

        // Tables: detect lines with | separators
        text = text.replace(/((?:^\|.+\|$\n?)+)/gm, function (block) {
            var rows = block.trim().split('\n');
            if (rows.length < 2) return block;
            var html = '<table class="md-table">';
            for (var r = 0; r < rows.length; r++) {
                var row = rows[r].trim();
                if (!row) continue;
                // Skip separator row (|---|---|)
                if (/^\|[\s\-:|]+\|$/.test(row)) continue;
                var cells = row.split('|').filter(function (c, idx, arr) { return idx > 0 && idx < arr.length - 1; });
                var tag = r === 0 ? 'th' : 'td';
                html += '<tr>';
                for (var c = 0; c < cells.length; c++) {
                    html += '<' + tag + '>' + cells[c].trim() + '</' + tag + '>';
                }
                html += '</tr>';
            }
            html += '</table>';
            return html;
        });

        // Unordered lists (- item or * item)
        text = text.replace(/^(\s*[-*] .+(\n|$))+/gm, function (block) {
            var items = block.trim().split('\n');
            var html = '<ul class="md-list">';
            for (var li = 0; li < items.length; li++) {
                html += '<li>' + items[li].replace(/^\s*[-*] /, '') + '</li>';
            }
            html += '</ul>';
            return html;
        });

        // Ordered lists (1. item)
        text = text.replace(/^(\s*\d+\. .+(\n|$))+/gm, function (block) {
            var items = block.trim().split('\n');
            var html = '<ol class="md-list">';
            for (var li = 0; li < items.length; li++) {
                html += '<li>' + items[li].replace(/^\s*\d+\. /, '') + '</li>';
            }
            html += '</ol>';
            return html;
        });

        // Linkify URLs
        text = text.replace(/(https?:\/\/[^\s<&]+)/g, '<a href="$1" target="_blank" rel="noopener noreferrer">$1</a>');

        // Line breaks (preserve newlines outside of block elements)
        text = text.replace(/\n/g, '<br>');
        // Clean up extra <br> after block elements
        text = text.replace(/(<\/pre>)<br>/g, '$1');
        text = text.replace(/(<\/table>)<br>/g, '$1');
        text = text.replace(/(<\/ul>)<br>/g, '$1');
        text = text.replace(/(<\/ol>)<br>/g, '$1');
        text = text.replace(/(<hr[^>]*>)<br>/g, '$1');

        return text;
    }


    window.toggleSources = function (id, btn) {
        var list = document.getElementById(id);
        if (!list) return;
        list.classList.toggle('open');
        if (btn) btn.classList.toggle('open');
    };

    window.handleNotSatisfied = function (btn, msgIndex) {
        // Find the corresponding user question (the message before this system answer)
        var userMsg = null;
        for (var i = msgIndex - 1; i >= 0; i--) {
            if (chatMessages[i].role === 'user') {
                userMsg = chatMessages[i];
                break;
            }
        }
        if (!userMsg) return;

        // Show confirmation dialog
        var overlay = document.createElement('div');
        overlay.className = 'chat-confirm-overlay';
        overlay.innerHTML =
            '<div class="chat-confirm-dialog">' +
                '<p>' + i18n.t('chat_not_satisfied_confirm') + '</p>' +
                '<div class="chat-confirm-actions">' +
                    '<button class="chat-confirm-yes">' + i18n.t('chat_not_satisfied_confirm_yes') + '</button>' +
                    '<button class="chat-confirm-no">' + i18n.t('chat_not_satisfied_confirm_no') + '</button>' +
                '</div>' +
            '</div>';
        document.body.appendChild(overlay);

        overlay.querySelector('.chat-confirm-no').onclick = function () {
            document.body.removeChild(overlay);
        };
        overlay.querySelector('.chat-confirm-yes').onclick = function () {
            document.body.removeChild(overlay);
            btn.disabled = true;
            btn.textContent = '...';

            var token = getChatToken();
            var reqBody = {
                question: userMsg.content,
                user_id: getChatUserID(),
                product_id: localStorage.getItem('askflow_product_id') || ''
            };
            if (userMsg.imageUrl) {
                reqBody.image_data = userMsg.imageUrl;
            }

            fetch('/api/pending/create', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': 'Bearer ' + token
                },
                body: JSON.stringify(reqBody)
            })
            .then(function (res) {
                if (!res.ok) {
                    // Handle 401 Unauthorized - session expired or invalid
                    if (res.status === 401) {
                        clearSession();
                        navigate('/login');
                        throw new Error(i18n.t('session_expired') || '会话已过期，请重新登录');
                    }
                    throw new Error('failed');
                }
                return res.json();
            })
            .then(function () {
                // Replace the answer message with a success message
                chatMessages[msgIndex] = {
                    role: 'system',
                    content: i18n.t('chat_not_satisfied_success'),
                    sources: [],
                    isPending: true,
                    timestamp: Date.now()
                };
                renderChatMessages();
            })
            .catch(function () {
                btn.disabled = false;
                btn.textContent = i18n.t('chat_not_satisfied');
                alert(i18n.t('chat_not_satisfied_fail'));
            });
        };
    };

    // Helper: add/remove progress overlay on an element
    function addProgressOverlay(container, indeterminate, id) {
        var overlay = document.createElement('div');
        overlay.className = 'img-progress-overlay';
        if (id) overlay.id = id;
        var bar = document.createElement('div');
        bar.className = 'img-progress-bar' + (indeterminate ? ' indeterminate' : '');
        overlay.appendChild(bar);
        container.appendChild(overlay);
        return bar;
    }

    function removeProgressOverlay(container, id) {
        var el = id ? document.getElementById(id) : container.querySelector('.img-progress-overlay');
        if (el) el.remove();
    }

    function setProgressBar(bar, pct) {
        if (bar) {
            bar.classList.remove('indeterminate');
            bar.style.width = Math.min(pct, 100) + '%';
        }
    }

    // Helper: set a button to loading state with spinner
    function setBtnLoading(btn, loadingText) {
        if (!btn) return;
        btn._origHTML = btn.innerHTML;
        btn.disabled = true;
        btn.classList.add('btn-loading');
        var label = loadingText || btn.textContent;
        btn.innerHTML = '<span class="btn-spinner"></span><span class="btn-label">' + escapeHtml(label) + '</span>';
    }

    // Helper: restore a button from loading state
    function resetBtnLoading(btn) {
        if (!btn) return;
        btn.disabled = false;
        btn.classList.remove('btn-loading');
        if (btn._origHTML !== undefined) {
            btn.innerHTML = btn._origHTML;
            delete btn._origHTML;
        }
    }

    window.sendChatMessage = function () {
        var input = document.getElementById('chat-input');
        var sendBtn = document.getElementById('chat-send-btn');
        if (!input) return;

        var question = input.value.trim();
        var imageData = chatPendingImage;
        if ((!question && !imageData) || chatLoading) return;

        // Default question text if only image
        if (!question && imageData) {
            question = i18n.t('chat_image_recognize');
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
        var chatPreview = document.getElementById('chat-image-preview');
        var hadImage = !!imageData;
        input.value = '';
        input.style.height = 'auto';
        chatPendingImage = null;
        if (sendBtn) sendBtn.disabled = true;

        // Always hide the chat image preview immediately after sending
        if (chatPreview) chatPreview.classList.add('hidden');

        // Show loading
        chatLoading = true;
        renderChatMessages();

        // Build request body
        var reqBody = {
            question: question,
            user_id: getChatUserID(),
            product_id: localStorage.getItem('askflow_product_id') || ''
        };
        if (imageData) {
            reqBody.image_data = imageData;
        }

        // Call API
        var token = getChatToken();
        var controller = new AbortController();
        var timeoutId = setTimeout(function () { controller.abort(); }, 90000); // 90s timeout

        fetch('/api/query', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Authorization': 'Bearer ' + token
            },
            body: JSON.stringify(reqBody),
            signal: controller.signal
        })
        .then(function (res) {
            if (!res.ok) {
                // Handle 401 Unauthorized - session expired or invalid
                if (res.status === 401) {
                    clearSession();
                    navigate('/login');
                    throw new Error(i18n.t('session_expired') || '会话已过期，请重新登录');
                }
                return res.text().then(function (text) {
                    try {
                        var d = JSON.parse(text);
                        throw new Error(d.error || i18n.t('chat_request_failed'));
                    } catch (e) {
                        if (e.message && e.message !== 'Unexpected token') throw e;
                        throw new Error(i18n.t('chat_request_failed'));
                    }
                });
            }
            return res.json();
        })
        .then(function (data) {
            var msg = {
                role: 'system',
                content: data.answer || data.message || i18n.t('chat_no_answer'),
                sources: data.sources || [],
                isPending: !!data.is_pending,
                allowDownload: !!data.allow_download,
                debugInfo: data.debug_info || null,
                timestamp: Date.now()
            };
            if (data.is_pending) {
                msg.content = data.message || i18n.t('chat_pending_message');
            }
            chatMessages.push(msg);
        })
        .catch(function (err) {
            var errMsg = err.name === 'AbortError'
                ? i18n.t('chat_error_prefix') + i18n.t('chat_error_timeout') + i18n.t('chat_error_suffix')
                : i18n.t('chat_error_prefix') + (err.message || i18n.t('chat_error_unknown')) + i18n.t('chat_error_suffix');
            chatMessages.push({
                role: 'system',
                content: errMsg,
                sources: [],
                isPending: false,
                isError: true,
                timestamp: Date.now()
            });
        })
        .finally(function () {
            clearTimeout(timeoutId);
            chatLoading = false;
            renderChatMessages();
            updateSendBtnState();
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
    var adminPermissions = []; // e.g. ['batch_import']

    function getAdminToken() {
        var session = getAdminSession();
        return session ? session.id || session.session_id || '' : '';
    }

    function adminFetch(url, options) {
        options = options || {};
        options.headers = options.headers || {};
        options.headers['Authorization'] = 'Bearer ' + getAdminToken();
        return fetch(url, options).then(function (res) {
            if (res.status === 401) {
                // Session expired or invalid — redirect to login
                clearAdminSession();
                showAdminToast(i18n.t('admin_session_expired') || '会话已过期，请重新登录', 'error');
                setTimeout(function () { navigate(adminLoginRoute || '/admin'); }, 1500);
            }
            return res;
        });
    }

    function downloadDocument(docId, fileName) {
        adminFetch('/api/documents/' + encodeURIComponent(docId) + '/download')
            .then(function(resp) {
                if (!resp.ok) throw new Error('Download failed');
                return resp.blob();
            })
            .then(function(blob) {
                var url = URL.createObjectURL(blob);
                var a = document.createElement('a');
                a.href = url;
                a.download = fileName || 'document';
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
            })
            .catch(function(err) {
                showAdminToast('Download failed: ' + err.message, 'error');
            });
    }
    window.downloadDocument = downloadDocument;

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
        if (tab === 'documents') { loadAdminProductSelectors().then(function() { loadDocumentList(); }); }
        if (tab === 'pending') loadPendingQuestions();
        if (tab === 'knowledge') loadAdminProductSelectors();
        if (tab === 'settings') { loadAdminSettings(); i18n.applyI18nToPage(); }
        if (tab === 'multimodal') { loadMultimodalSettings(); i18n.applyI18nToPage(); }
        if (tab === 'users') { loadAdminUsers(); loadProductCheckboxes(); i18n.applyI18nToPage(); }
        if (tab === 'products') { loadProducts(); i18n.applyI18nToPage(); }
        if (tab === 'bans') loadLoginBans();
        if (tab === 'customers') { loadAdminCustomers(); i18n.applyI18nToPage(); }
        if (tab === 'batchimport') { loadBatchImportProductSelector(); }
    };

    // --- Settings Sub-Tab Switching ---
    window.switchSettingsSubTab = function (tabId) {
        var container = document.getElementById('admin-tab-settings');
        if (!container) return;
        // Update tab buttons
        var btns = container.querySelectorAll('.settings-tab-btn');
        btns.forEach(function (btn) {
            btn.classList.toggle('active', btn.getAttribute('data-settings-tab') === tabId);
        });
        // Update panels
        var panels = container.querySelectorAll('.settings-panel');
        panels.forEach(function (p) {
            p.classList.add('hidden');
            p.classList.remove('settings-panel-animated');
        });
        var target = document.getElementById('settings-panel-' + tabId);
        if (target) {
            target.classList.remove('hidden');
            target.classList.add('settings-panel-animated');
        }
        // Auto-load logs when switching to logs tab
        if (tabId === 'settings-logs') {
            loadRecentLogs();
        }
    };

    // --- SMTP Presets ---
    var smtpPresets = {
        qq:      { host: 'smtp.qq.com',             port: 465, tls: 'true',  auth: 'LOGIN' },
        '163':   { host: 'smtp.163.com',            port: 465, tls: 'true',  auth: 'LOGIN' },
        gmail:   { host: 'smtp.gmail.com',          port: 587, tls: 'true',  auth: 'PLAIN' },
        outlook: { host: 'smtp.office365.com',      port: 587, tls: 'true',  auth: 'PLAIN' },
        aliyun:  { host: 'smtp.qiye.aliyun.com',    port: 465, tls: 'true',  auth: 'LOGIN' },
        exmail:  { host: 'smtp.exmail.qq.com',      port: 465, tls: 'true',  auth: 'LOGIN' }
    };

    window.applySmtpPreset = function (provider) {
        var p = smtpPresets[provider];
        if (!p) return;
        var hostEl = document.getElementById('cfg-smtp-host');
        var portEl = document.getElementById('cfg-smtp-port');
        var tlsEl  = document.getElementById('cfg-smtp-tls');
        var authEl = document.getElementById('cfg-smtp-auth-method');
        if (hostEl) hostEl.value = p.host;
        if (portEl) portEl.value = p.port;
        if (tlsEl)  tlsEl.value  = p.tls;
        if (authEl) authEl.value = p.auth;
        showAdminToast(i18n.t('admin_settings_smtp_preset_applied', { provider: provider.toUpperCase() }), 'success');
    };

    // --- Customer Management ---

    var pendingBanEmail = '';
    var customerPage = 1;
    var customerPageSize = 20;
    var customerSearch = '';

    window.loadAdminCustomers = function (page, search) {
        var tbody = document.getElementById('admin-customers-tbody');
        if (!tbody) return;

        if (typeof page === 'number') customerPage = page;
        if (typeof search === 'string') customerSearch = search;

        var url = '/api/admin/customers?page=' + customerPage + '&page_size=' + customerPageSize;
        if (customerSearch) url += '&search=' + encodeURIComponent(customerSearch);

        adminFetch(url)
            .then(function (res) {
                if (!res.ok) throw new Error('HTTP ' + res.status);
                return res.json();
            })
            .then(function (data) {
                var customers = data.customers || [];
                var total = data.total || 0;
                var bannedCount = data.banned_count || 0;

                // Update stats
                var totalEl = document.getElementById('admin-customers-total-count');
                var bannedEl = document.getElementById('admin-customers-banned-count');
                if (totalEl) totalEl.textContent = total;
                if (bannedEl) bannedEl.textContent = bannedCount;

                if (customers.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="6" class="admin-table-empty">' + i18n.t('admin_customers_empty') + '</td></tr>';
                    renderCustomerPagination(0, 1);
                    return;
                }

                tbody.innerHTML = '';
                customers.forEach(function (c) {
                    var tr = document.createElement('tr');
                    
                    var statusText = '';
                    var statusClass = '';
                    if (c.is_banned) {
                        statusText = i18n.t('admin_customers_status_banned');
                        statusClass = 'status-failed';
                    } else if (c.email_verified) {
                        statusText = i18n.t('admin_customers_status_verified');
                        statusClass = 'status-success';
                    } else {
                        statusText = i18n.t('admin_customers_status_unverified');
                        statusClass = 'status-processing';
                    }

                    var actions = '';
                    if (!c.email_verified && !c.is_banned) {
                        actions += '<button class="btn-table btn-secondary" onclick="handleVerifyCustomer(\'' + c.id + '\')">' + i18n.t('admin_customers_verify_btn') + '</button>';
                    }
                    
                    if (c.is_banned) {
                        actions += '<button class="btn-table btn-secondary" onclick="handleUnbanCustomer(\'' + c.email + '\')">' + i18n.t('admin_customers_unban_btn') + '</button>';
                    } else {
                        actions += '<button class="btn-table btn-secondary" onclick="handleBanCustomer(\'' + c.email + '\')">' + i18n.t('admin_customers_ban_btn') + '</button>';
                    }
                    
                    actions += '<button class="btn-table btn-danger" onclick="handleDeleteCustomer(\'' + c.id + '\')">' + i18n.t('admin_customers_delete_btn') + '</button>';

                    tr.innerHTML = 
                        '<td>' + (escapeHtml(c.name) || '--') + '</td>' +
                        '<td>' + (escapeHtml(c.email) || '--') + '</td>' +
                        '<td><span class="status-badge ' + statusClass + '">' + statusText + '</span></td>' +
                        '<td>' + (c.created_at || '--') + '</td>' +
                        '<td>' + (c.last_login || '--') + '</td>' +
                        '<td class="admin-table-actions">' + actions + '</td>';
                    tbody.appendChild(tr);
                });

                renderCustomerPagination(total, customerPage);
                i18n.applyI18nToPage();
            })
            .catch(function (err) {
                console.error('Load customers error:', err);
                showAdminToast(i18n.t('admin_doc_load_failed'), 'error');
            });
    };

    function renderCustomerPagination(total, currentPage) {
        var container = document.getElementById('admin-customers-pagination');
        if (!container) return;
        var totalPages = Math.max(1, Math.ceil(total / customerPageSize));
        if (totalPages <= 1) {
            container.innerHTML = '';
            return;
        }
        var html = '';
        // Prev button
        html += '<button class="btn-secondary" style="padding:0.3rem 0.7rem;font-size:0.85rem;" ' +
            (currentPage <= 1 ? 'disabled' : 'onclick="loadAdminCustomers(' + (currentPage - 1) + ')"') + '>&laquo;</button>';
        // Page numbers (show max 7 pages around current)
        var startP = Math.max(1, currentPage - 3);
        var endP = Math.min(totalPages, currentPage + 3);
        if (startP > 1) html += '<span style="padding:0 0.3rem;">...</span>';
        for (var p = startP; p <= endP; p++) {
            if (p === currentPage) {
                html += '<button class="btn-secondary" style="padding:0.3rem 0.7rem;font-size:0.85rem;background:#4F46E5;color:#fff;border-color:#4F46E5;" disabled>' + p + '</button>';
            } else {
                html += '<button class="btn-secondary" style="padding:0.3rem 0.7rem;font-size:0.85rem;" onclick="loadAdminCustomers(' + p + ')">' + p + '</button>';
            }
        }
        if (endP < totalPages) html += '<span style="padding:0 0.3rem;">...</span>';
        // Next button
        html += '<button class="btn-secondary" style="padding:0.3rem 0.7rem;font-size:0.85rem;" ' +
            (currentPage >= totalPages ? 'disabled' : 'onclick="loadAdminCustomers(' + (currentPage + 1) + ')"') + '>&raquo;</button>';
        container.innerHTML = html;
    }

    window.searchCustomers = function () {
        var input = document.getElementById('admin-customers-search');
        customerSearch = input ? input.value.trim() : '';
        customerPage = 1;
        loadAdminCustomers(1, customerSearch);
    };

    window.clearCustomerSearch = function () {
        var input = document.getElementById('admin-customers-search');
        if (input) input.value = '';
        customerSearch = '';
        customerPage = 1;
        loadAdminCustomers(1, '');
    };

    window.handleVerifyCustomer = function (userId) {
        if (!confirm(i18n.t('admin_customer_verify_confirm'))) return;
        adminFetch('/api/admin/customers/verify', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ user_id: userId })
        })
        .then(function (res) {
            if (!res.ok) throw new Error('Failed');
            showAdminToast(i18n.t('admin_settings_saved'), 'success');
            loadAdminCustomers();
        })
        .catch(function () { showAdminToast(i18n.t('admin_settings_save_failed'), 'error'); });
    };

    window.handleDeleteCustomer = function (userId) {
        if (!confirm(i18n.t('admin_customer_delete_confirm'))) return;
        adminFetch('/api/admin/customers/delete', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ user_id: userId })
        })
        .then(function (res) {
            if (!res.ok) throw new Error('Failed');
            showAdminToast(i18n.t('admin_delete_success'), 'success');
            loadAdminCustomers();
        })
        .catch(function () { showAdminToast(i18n.t('admin_delete_failed'), 'error'); });
    };

    window.handleBanCustomer = function (email) {
        pendingBanEmail = email;
        var dialog = document.getElementById('admin-customer-ban-dialog');
        var msg = document.getElementById('admin-customer-ban-msg');
        if (msg) msg.textContent = email;
        if (dialog) dialog.classList.remove('hidden');
    };

    window.closeCustomerBanDialog = function () {
        var dialog = document.getElementById('admin-customer-ban-dialog');
        if (dialog) dialog.classList.add('hidden');
    };

    window.confirmCustomerBan = function () {
        var reason = document.getElementById('admin-customer-ban-reason').value.trim();
        var days = parseInt(document.getElementById('admin-customer-ban-days').value);
        
        adminFetch('/api/admin/customers/ban', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: pendingBanEmail, reason: reason, days: days })
        })
        .then(function (res) {
            if (!res.ok) throw new Error('Failed');
            showAdminToast(i18n.t('admin_bans_added'), 'success');
            closeCustomerBanDialog();
            loadAdminCustomers();
        })
        .catch(function () { showAdminToast(i18n.t('admin_bans_add_failed'), 'error'); });
    };

    window.handleUnbanCustomer = function (email) {
        adminFetch('/api/admin/customers/unban', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: email })
        })
        .then(function (res) {
            if (!res.ok) throw new Error('Failed');
            showAdminToast(i18n.t('admin_bans_unbanned'), 'success');
            loadAdminCustomers();
        })
        .catch(function () { showAdminToast(i18n.t('admin_bans_unban_failed'), 'error'); });
    };

    function initAdmin() {
        // Display current admin username
        var adminUser = getAdminUser();
        var usernameDisplay = document.getElementById('admin-username-display');
        if (usernameDisplay && adminUser && adminUser.username) {
            usernameDisplay.textContent = adminUser.username;
        }

        setupDropZone();
        initKnowledgeImageZone();
        initKnowledgeVideoZone();
        // Fetch role and apply visibility
        adminFetch('/api/admin/role')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                adminRole = data.role || '';
                adminPermissions = data.permissions || [];
                applyAdminRoleVisibility();
            })
            .catch(function () {
                adminRole = localStorage.getItem('admin_role') || '';
                adminPermissions = [];
                applyAdminRoleVisibility();
            })
            .finally(function () {
                switchAdminTab('documents');
            });
    }

    function applyAdminRoleVisibility() {
        // Hide settings, users, products, and bans tabs for non-super_admin
        var settingsNav = document.querySelector('.admin-nav-item[data-tab="settings"]');
        var usersNav = document.querySelector('.admin-nav-item[data-tab="users"]');
        var productsNav = document.querySelector('.admin-nav-item[data-tab="products"]');
        var bansNav = document.querySelector('.admin-nav-item[data-tab="bans"]');
        var customersNav = document.querySelector('.admin-nav-item[data-tab="customers"]');
        var batchimportNav = document.querySelector('.admin-nav-item[data-tab="batchimport"]');
        if (adminRole !== 'super_admin') {
            if (settingsNav) settingsNav.style.display = 'none';
            if (usersNav) usersNav.style.display = 'none';
            if (productsNav) productsNav.style.display = 'none';
            if (bansNav) bansNav.style.display = 'none';
            if (customersNav) customersNav.style.display = 'none';
            // Batch import: only show if editor has batch_import permission
            var hasBatchImport = adminPermissions.indexOf('batch_import') !== -1;
            if (batchimportNav) batchimportNav.style.display = hasBatchImport ? '' : 'none';
        } else {
            if (settingsNav) settingsNav.style.display = '';
            if (usersNav) usersNav.style.display = '';
            if (productsNav) productsNav.style.display = '';
            if (bansNav) bansNav.style.display = '';
            if (customersNav) customersNav.style.display = '';
            if (batchimportNav) batchimportNav.style.display = '';
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
        // Check file size against configured max upload size
        if (file.size > maxUploadSizeMB * 1024 * 1024) {
            showAdminToast(i18n.t('admin_doc_upload_failed') + ' - ' + i18n.t('video_size_error', { size: maxUploadSizeMB }), 'error');
            return;
        }
        var formData = new FormData();
        formData.append('file', file);
        formData.append('product_id', getDocProductID());

        showAdminToast(i18n.t('admin_doc_uploading', { name: file.name }), 'info');

        // Show progress bar on drop zone and set uploading state
        var zone = document.getElementById('admin-drop-zone');
        var progressBar = null;
        if (zone) {
            removeProgressOverlay(zone);
            progressBar = addProgressOverlay(zone, false, 'upload-drop-progress');
            zone.classList.add('uploading');
            zone.style.pointerEvents = 'none';
        }

        var token = getAdminToken();
        var xhr = new XMLHttpRequest();
        xhr.open('POST', '/api/documents/upload', true);
        xhr.setRequestHeader('Authorization', 'Bearer ' + token);

        xhr.upload.onprogress = function (e) {
            if (e.lengthComputable && progressBar) {
                setProgressBar(progressBar, (e.loaded / e.total) * 90);
            }
        };

        xhr.onload = function () {
            if (progressBar) setProgressBar(progressBar, 100);
            setTimeout(function () {
                if (zone) {
                    removeProgressOverlay(zone, 'upload-drop-progress');
                    zone.classList.remove('uploading');
                    zone.style.pointerEvents = '';
                }
            }, 400);
            if (xhr.status >= 200 && xhr.status < 300) {
                try {
                    var resp = JSON.parse(xhr.responseText);
                    if (resp && resp.status === 'failed') {
                        showAdminToast(i18n.t('admin_doc_upload_failed') + (resp.error ? ' - ' + resp.error : ''), 'error');
                    } else {
                        var msg = i18n.t('admin_doc_upload_success');
                        if (resp && resp.stats) {
                            msg += ' - ' + i18n.t('admin_doc_upload_stats', { chars: resp.stats.text_chars, images: resp.stats.image_count });
                        }
                        showAdminToast(msg, 'success');
                    }
                } catch (e) {
                    showAdminToast(i18n.t('admin_doc_upload_success'), 'success');
                }
                loadDocumentList();
            } else {
                var errMsg = '';
                try {
                    var errResp = JSON.parse(xhr.responseText);
                    errMsg = errResp.error || errResp.message || '';
                } catch (e) {}
                showAdminToast(i18n.t('admin_doc_upload_failed') + (errMsg ? ' - ' + errMsg : ''), 'error');
            }
        };

        xhr.onerror = function () {
            if (zone) {
                removeProgressOverlay(zone, 'upload-drop-progress');
                zone.classList.remove('uploading');
                zone.style.pointerEvents = '';
            }
            showAdminToast(i18n.t('admin_doc_upload_failed'), 'error');
        };

        xhr.send(formData);
    }

    window.handleAdminURLPreview = function () {
        var input = document.getElementById('admin-url-field');
        var btn = document.getElementById('admin-url-preview-btn');
        var spinner = document.getElementById('admin-url-spinner');
        if (!input) return;
        var url = input.value.trim();
        if (!url) {
            showAdminToast(i18n.t('admin_doc_url_empty'), 'error');
            return;
        }

        if (btn) btn.disabled = true;
        if (spinner) spinner.classList.remove('hidden');
        showAdminToast(i18n.t('admin_doc_url_fetching'), 'info');

        adminFetch('/api/documents/url/preview', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ url: url })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_doc_url_fetch_failed')); });
            return res.json();
        })
        .then(function (data) {
            var previewArea = document.getElementById('admin-url-preview-area');
            var previewContent = document.getElementById('admin-url-preview-content');
            if (previewContent) previewContent.textContent = data.text || '';
            if (previewArea) previewArea.classList.remove('hidden');
            showAdminToast(i18n.t('admin_doc_url_fetched'), 'success');
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_doc_url_fetch_failed'), 'error');
        })
        .finally(function () {
            if (btn) btn.disabled = false;
            if (spinner) spinner.classList.add('hidden');
        });
    };

    window.handleAdminURLConfirm = function () {
        var input = document.getElementById('admin-url-field');
        var confirmBtn = document.getElementById('admin-url-confirm-btn');
        var spinner = document.getElementById('admin-url-confirm-spinner');
        if (!input) return;
        var url = input.value.trim();
        if (!url) return;

        if (confirmBtn) confirmBtn.disabled = true;
        if (spinner) spinner.classList.remove('hidden');
        showAdminToast(i18n.t('admin_doc_url_submitting'), 'info');

        adminFetch('/api/documents/url', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ url: url, product_id: getDocProductID() })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_doc_url_failed')); });
            return res.json();
        })
        .then(function (resp) {
            var msg = i18n.t('admin_doc_url_success');
            if (resp && resp.stats) {
                msg += ' - ' + i18n.t('admin_doc_url_stats', { chars: resp.stats.text_chars, images: resp.stats.image_count });
            }
            showAdminToast(msg, 'success');
            input.value = '';
            handleAdminURLCancel();
            loadDocumentList();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_doc_url_failed'), 'error');
        })
        .finally(function () {
            if (confirmBtn) confirmBtn.disabled = false;
            if (spinner) spinner.classList.add('hidden');
        });
    };

    window.handleAdminURLCancel = function () {
        var previewArea = document.getElementById('admin-url-preview-area');
        if (previewArea) previewArea.classList.add('hidden');
    };

    // --- Admin Product Selectors (for documents & knowledge) ---

    var adminProductsCache = null;

    function loadAdminProductSelectors() {
        return adminFetch('/api/products/my')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                adminProductsCache = data.products || [];
                populateProductSelect('doc-product-select', adminProductsCache);
                populateProductSelect('knowledge-product-select', adminProductsCache);
            })
            .catch(function () {
                adminProductsCache = [];
            });
    }

    function populateProductSelect(selectId, products) {
        var select = document.getElementById(selectId);
        if (!select) return;
        var currentVal = select.value;
        select.innerHTML = '<option value="">' + i18n.t('admin_doc_product_public') + '</option>';
        for (var i = 0; i < products.length; i++) {
            var opt = document.createElement('option');
            opt.value = products[i].id;
            opt.textContent = products[i].name;
            select.appendChild(opt);
        }
        // Restore previous selection if still valid
        if (currentVal) select.value = currentVal;
    }

    function getDocProductID() {
        var select = document.getElementById('doc-product-select');
        return select ? select.value : '';
    }

    function getKnowledgeProductID() {
        var select = document.getElementById('knowledge-product-select');
        return select ? select.value : '';
    }

    function getProductNameByID(productId) {
        if (!productId) return i18n.t('admin_doc_product_public');
        if (!adminProductsCache) return productId;
        for (var i = 0; i < adminProductsCache.length; i++) {
            if (adminProductsCache[i].id === productId) return adminProductsCache[i].name;
        }
        return productId;
    }

    function loadDocumentList() {
        adminFetch('/api/documents')
            .then(function (res) {
                if (!res.ok) throw new Error(i18n.t('admin_doc_load_failed'));
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
            tbody.innerHTML = '<tr><td colspan="6" class="admin-table-empty">' + i18n.t('admin_doc_empty') + '</td></tr>';
            return;
        }

        var html = '';
        for (var i = 0; i < docs.length; i++) {
            var doc = docs[i];
            var statusClass = 'admin-badge-' + (doc.status || 'processing');
            var statusMap = { processing: i18n.t('admin_doc_status_processing'), success: i18n.t('admin_doc_status_success'), failed: i18n.t('admin_doc_status_failed') };
            var statusText = statusMap[doc.status] || doc.status;
            var timeStr = doc.created_at ? new Date(doc.created_at).toLocaleString(i18n.getLang()) : '-';
            var productName = getProductNameByID(doc.product_id || '');

            var nameCell = '';
            if (doc.type === 'url') {
                nameCell = '<a href="' + escapeHtml(doc.name) + '" target="_blank">' + escapeHtml(doc.name || '-') + '</a>';
            } else if (doc.type === 'answer') {
                nameCell = escapeHtml(doc.name || '-');
            } else {
                nameCell = '<a href="javascript:void(0)" onclick="downloadDocument(\'' + escapeHtml(doc.id) + '\', \'' + escapeHtml(doc.name || 'document') + '\')">' + escapeHtml(doc.name || '-') + '</a>';
            }

            html += '<tr>' +
                '<td>' + nameCell + '</td>' +
                '<td>' + escapeHtml(productName) + '</td>' +
                '<td>' + escapeHtml(doc.type || '-') + '</td>' +
                '<td><span class="admin-badge ' + statusClass + '">' + escapeHtml(statusText) + '</span></td>' +
                '<td>' + escapeHtml(timeStr) + '</td>' +
                '<td><button class="btn-danger btn-sm" onclick="showDeleteDialog(\'' + escapeHtml(doc.id) + '\', \'' + escapeHtml(doc.name || '') + '\')">' + i18n.t('admin_doc_delete_btn') + '</button></td>' +
            '</tr>';
        }
        tbody.innerHTML = html;
    }

    // --- Delete Document ---

    window.showDeleteDialog = function (docId, docName) {
        adminDeleteTargetId = docId;
        var msg = document.getElementById('admin-confirm-msg');
        if (msg) msg.textContent = i18n.t('admin_delete_msg', { name: docName });
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
            if (!res.ok) throw new Error(i18n.t('admin_delete_failed'));
            showAdminToast(i18n.t('admin_delete_success'), 'success');
            loadDocumentList();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_delete_failed'), 'error');
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
                if (!res.ok) throw new Error(i18n.t('admin_doc_load_failed'));
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
            container.innerHTML = '<div class="admin-table-empty">' + i18n.t('admin_pending_empty') + '</div>';
            return;
        }

        var html = '';
        for (var i = 0; i < questions.length; i++) {
            var q = questions[i];
            var statusClass = 'admin-badge-' + (q.status || 'pending');
            var statusText = q.status === 'answered' ? i18n.t('admin_pending_filter_answered') : i18n.t('admin_pending_filter_pending');
            var timeStr = q.created_at ? new Date(q.created_at).toLocaleString(i18n.getLang()) : '-';

            html += '<div class="admin-pending-card">';
            html += '<div class="admin-pending-card-header">';
            html += '<div class="admin-pending-meta">';
            html += '<span>' + i18n.t('admin_pending_user') + ': ' + escapeHtml(q.user_id || '-') + '</span>';
            html += '<span>' + escapeHtml(timeStr) + '</span>';
            if (q.product_name) {
                html += '<span style="background:#EEF2FF;color:#4F46E5;padding:2px 8px;border-radius:4px;font-size:0.8rem;">' + escapeHtml(q.product_name) + '</span>';
            } else {
                html += '<span style="background:#F3F4F6;color:#6B7280;padding:2px 8px;border-radius:4px;font-size:0.8rem;">' + i18n.t('admin_doc_product_public') + '</span>';
            }
            html += '</div>';
            html += '<span class="admin-badge ' + statusClass + '">' + escapeHtml(statusText) + '</span>';
            html += '</div>';
            html += '<div class="admin-pending-question">' + escapeHtml(q.question || '') + '</div>';

            if (q.image_data) {
                html += '<div class="admin-pending-image" style="margin:8px 0"><img src="' + escapeHtml(q.image_data) + '" style="max-width:300px;max-height:200px;border-radius:6px;border:1px solid #e0e0e0;cursor:pointer" onclick="window.open(this.src)" alt="' + i18n.t('chat_user_image_alt') + '" /></div>';
            }

            if (q.answer) {
                html += '<div class="admin-pending-answer-preview">' + i18n.t('admin_pending_answer_prefix') + ': ' + escapeHtml(q.answer) + '</div>';
            }

            if (q.status !== 'answered') {
                html += '<button class="btn-primary btn-sm admin-answer-btn" data-id="' + escapeHtml(q.id) + '" data-question="' + escapeHtml(q.question || '') + '" data-image="' + escapeHtml(q.image_data || '') + '">' + i18n.t('admin_pending_answer_btn') + '</button>';
            } else {
                html += '<button class="btn-secondary btn-sm admin-edit-answer-btn" data-id="' + escapeHtml(q.id) + '" data-question="' + escapeHtml(q.question || '') + '" data-answer="' + escapeHtml(q.answer || '') + '" data-image="' + escapeHtml(q.image_data || '') + '">' + i18n.t('admin_pending_edit_btn') + '</button>';
            }

            html += ' <button class="btn-danger btn-sm admin-delete-pending-btn" data-id="' + escapeHtml(q.id) + '">' + i18n.t('admin_pending_delete_btn') + '</button>';

            html += '</div>';
        }
        container.innerHTML = html;

        // Bind answer button clicks
        var answerBtns = container.querySelectorAll('.admin-answer-btn');
        for (var j = 0; j < answerBtns.length; j++) {
            (function(btn) {
                btn.addEventListener('click', function() {
                    showAnswerDialog(btn.getAttribute('data-id'), btn.getAttribute('data-question'), null, btn.getAttribute('data-image'));
                });
            })(answerBtns[j]);
        }

        // Bind delete button clicks
        var deleteBtns = container.querySelectorAll('.admin-delete-pending-btn');
        for (var k = 0; k < deleteBtns.length; k++) {
            (function(btn) {
                btn.addEventListener('click', function() {
                    var qid = btn.getAttribute('data-id');
                    if (!confirm(i18n.t('admin_pending_delete_confirm'))) return;
                    adminFetch('/api/pending/' + encodeURIComponent(qid), { method: 'DELETE' })
                        .then(function(res) {
                            if (!res.ok) throw new Error(i18n.t('admin_delete_failed'));
                            showAdminToast(i18n.t('admin_pending_deleted'), 'success');
                            loadPendingQuestions();
                        })
                        .catch(function(err) {
                            showAdminToast(err.message || i18n.t('admin_delete_failed'), 'error');
                        });
                });
            })(deleteBtns[k]);
        }

        // Bind edit button clicks
        var editBtns = container.querySelectorAll('.admin-edit-answer-btn');
        for (var m = 0; m < editBtns.length; m++) {
            (function(btn) {
                btn.addEventListener('click', function() {
                    showAnswerDialog(
                        btn.getAttribute('data-id'),
                        btn.getAttribute('data-question'),
                        btn.getAttribute('data-answer'),
                        btn.getAttribute('data-image')
                    );
                });
            })(editBtns[m]);
        }
    }

    // --- Answer Dialog ---

    var answerImageURLs = [];
    var answerIsEdit = false;

    function initAnswerImageZone() {
        var area = document.getElementById('answer-image-upload-area');
        var input = document.getElementById('answer-image-input');
        if (!area || !input) return;

        area.onclick = function () { input.click(); };

        input.onchange = function () {
            if (input.files && input.files.length > 0) {
                for (var i = 0; i < input.files.length; i++) {
                    uploadAnswerImage(input.files[i]);
                }
                input.value = '';
            }
        };

        area.ondragover = function (e) { e.preventDefault(); area.classList.add('dragover'); };
        area.ondragleave = function () { area.classList.remove('dragover'); };
        area.ondrop = function (e) {
            e.preventDefault();
            area.classList.remove('dragover');
            var files = e.dataTransfer.files;
            for (var i = 0; i < files.length; i++) {
                if (files[i].type.indexOf('image/') === 0) uploadAnswerImage(files[i]);
            }
        };

        // Clipboard paste on the dialog
        var dialog = document.getElementById('admin-answer-dialog');
        if (dialog) {
            dialog.onpaste = function (e) {
                var items = (e.clipboardData || e.originalEvent.clipboardData || {}).items;
                if (!items) return;
                for (var i = 0; i < items.length; i++) {
                    if (items[i].type.indexOf('image/') === 0) {
                        e.preventDefault();
                        var blob = items[i].getAsFile();
                        if (blob) uploadAnswerImage(blob);
                    }
                }
            };
        }
    }

    function uploadAnswerImage(file) {
        if (file.type.indexOf('image/') !== 0) {
            showAdminToast(i18n.t('image_select_error'), 'error');
            return;
        }
        if (file.size > 10 * 1024 * 1024) {
            showAdminToast(i18n.t('image_size_error'), 'error');
            return;
        }

        var preview = document.getElementById('answer-image-preview');
        var item = document.createElement('div');
        item.className = 'knowledge-image-item uploading';
        var img = document.createElement('img');
        img.src = URL.createObjectURL(file);
        item.appendChild(img);
        preview.appendChild(item);

        // Add progress bar on this image item
        var progressBar = addProgressOverlay(item, false);

        var formData = new FormData();
        formData.append('image', file, file.name || 'paste.png');

        var token = getAdminToken();
        var xhr = new XMLHttpRequest();
        xhr.open('POST', '/api/images/upload', true);
        xhr.setRequestHeader('Authorization', 'Bearer ' + token);

        xhr.upload.onprogress = function (e) {
            if (e.lengthComputable && progressBar) {
                setProgressBar(progressBar, (e.loaded / e.total) * 90);
            }
        };

        xhr.onload = function () {
            if (progressBar) setProgressBar(progressBar, 100);
            setTimeout(function () { removeProgressOverlay(item); }, 300);
            item.classList.remove('uploading');

            if (xhr.status >= 200 && xhr.status < 300) {
                var data;
                try { data = JSON.parse(xhr.responseText); } catch (e) { data = {}; }
                var idx = answerImageURLs.length;
                answerImageURLs.push(data.url);

                var removeBtn = document.createElement('button');
                removeBtn.className = 'knowledge-image-remove';
                removeBtn.textContent = '×';
                removeBtn.setAttribute('aria-label', i18n.t('image_remove_label'));
                removeBtn.onclick = function () {
                    answerImageURLs[idx] = null;
                    item.remove();
                };
                item.appendChild(removeBtn);
            } else {
                item.remove();
                showAdminToast(i18n.t('image_upload_failed'), 'error');
            }
        };

        xhr.onerror = function () {
            item.remove();
            showAdminToast(i18n.t('image_upload_failed'), 'error');
        };

        xhr.send(formData);
    }

    window.showAnswerDialog = function (questionId, questionText, existingAnswer, imageData) {
        adminAnswerTargetId = questionId;
        answerIsEdit = !!existingAnswer;
        var textEl = document.getElementById('admin-answer-question-text');
        if (textEl) textEl.textContent = questionText;

        // Show question image if present
        var qImgEl = document.getElementById('admin-answer-question-image');
        if (!qImgEl) {
            // Create image container after question text element
            qImgEl = document.createElement('div');
            qImgEl.id = 'admin-answer-question-image';
            qImgEl.style.cssText = 'margin:8px 0';
            if (textEl && textEl.parentNode) {
                textEl.parentNode.insertBefore(qImgEl, textEl.nextSibling);
            }
        }
        if (imageData) {
            qImgEl.innerHTML = '<img src="' + imageData + '" style="max-width:100%;max-height:300px;border-radius:6px;border:1px solid #e0e0e0;cursor:pointer" onclick="window.open(this.src)" alt="' + i18n.t('chat_user_image_alt') + '" />';
            qImgEl.style.display = '';
        } else {
            qImgEl.innerHTML = '';
            qImgEl.style.display = 'none';
        }

        var answerInput = document.getElementById('admin-answer-text');
        if (answerInput) answerInput.value = existingAnswer || '';
        var urlInput = document.getElementById('admin-answer-url');
        if (urlInput) urlInput.value = '';
        answerImageURLs = [];
        var preview = document.getElementById('answer-image-preview');
        if (preview) preview.innerHTML = '';
        var dialog = document.getElementById('admin-answer-dialog');
        if (dialog) dialog.classList.remove('hidden');
        initAnswerImageZone();
    };

    window.closeAnswerDialog = function () {
        adminAnswerTargetId = null;
        answerIsEdit = false;
        answerImageURLs = [];
        var preview = document.getElementById('answer-image-preview');
        if (preview) preview.innerHTML = '';
        var dialog = document.getElementById('admin-answer-dialog');
        if (dialog) dialog.classList.add('hidden');
    };

    window.submitAdminAnswer = function () {
        if (!adminAnswerTargetId) return;

        var text = (document.getElementById('admin-answer-text') || {}).value || '';
        var url = (document.getElementById('admin-answer-url') || {}).value || '';
        var imageUrls = answerImageURLs.filter(function (u) { return u; });

        if (!text.trim() && !url.trim() && imageUrls.length === 0) {
            showAdminToast(i18n.t('admin_answer_empty'), 'error');
            return;
        }

        var submitBtn = document.getElementById('admin-answer-submit-btn');
        setBtnLoading(submitBtn, i18n.t('admin_knowledge_submitting_btn'));

        adminFetch('/api/pending/answer', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                question_id: adminAnswerTargetId,
                text: text.trim(),
                url: url.trim(),
                image_urls: imageUrls,
                is_edit: answerIsEdit
            })
        })
        .then(function (res) {
            if (!res.ok) throw new Error(i18n.t('admin_answer_failed'));
            showAdminToast(i18n.t('admin_answer_success'), 'success');
            closeAnswerDialog();
            loadPendingQuestions();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_answer_failed'), 'error');
        })
        .finally(function () {
            resetBtnLoading(submitBtn);
        });
    };

    // --- Settings ---

    function loadAdminSettings() {
        adminFetch('/api/config')
            .then(function (res) {
                if (!res.ok) throw new Error(i18n.t('admin_doc_load_failed'));
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
                setPlaceholder('cfg-llm-apikey', llm.api_key ? '***' : i18n.t('admin_settings_not_set'));
                setVal('cfg-llm-temperature', llm.temperature);
                setVal('cfg-llm-maxtokens', llm.max_tokens);

                setVal('cfg-emb-endpoint', emb.endpoint);
                setVal('cfg-emb-model', emb.model_name);
                setVal('cfg-emb-apikey', '');
                setPlaceholder('cfg-emb-apikey', emb.api_key ? '***' : i18n.t('admin_settings_not_set'));
                var mmSelect = document.getElementById('cfg-emb-multimodal');
                if (mmSelect) mmSelect.value = emb.use_multimodal ? 'true' : 'false';

                setVal('cfg-vec-chunksize', vec.chunk_size);
                setVal('cfg-vec-overlap', vec.overlap);
                setVal('cfg-vec-topk', vec.top_k);
                setVal('cfg-vec-threshold', vec.threshold);
                var cpSelect = document.getElementById('cfg-vec-content-priority');
                if (cpSelect) cpSelect.value = vec.content_priority || 'image_text';
                var tmSelect = document.getElementById('cfg-vec-text-match');
                if (tmSelect) tmSelect.value = vec.text_match_enabled === false ? 'false' : 'true';
                var dbgSelect = document.getElementById('cfg-vec-debug-mode');
                if (dbgSelect) dbgSelect.value = vec.debug_mode ? 'true' : 'false';

                setVal('cfg-admin-login-route', admin.login_route || '/admin');

                setVal('cfg-product-name', cfg.product_name || '');
                setVal('cfg-product-intro', cfg.product_intro || '');

                setVal('cfg-auth-server', cfg.auth_server || '');

                var smtp = cfg.smtp || {};
                setVal('cfg-smtp-host', smtp.host);
                setVal('cfg-smtp-port', smtp.port);
                setVal('cfg-smtp-username', smtp.username);
                setVal('cfg-smtp-password', '');
                setPlaceholder('cfg-smtp-password', smtp.password ? '***' : i18n.t('admin_settings_not_set'));
                setVal('cfg-smtp-from-addr', smtp.from_addr);
                setVal('cfg-smtp-from-name', smtp.from_name);
                var tlsSelect = document.getElementById('cfg-smtp-tls');
                if (tlsSelect) tlsSelect.value = smtp.use_tls === false ? 'false' : 'true';
                var authMethodSelect = document.getElementById('cfg-smtp-auth-method');
                if (authMethodSelect) authMethodSelect.value = smtp.auth_method || '';

                // Load OAuth providers
                renderOAuthProviderSettings(cfg.oauth || {});
            })
            .catch(function () {
                showAdminToast(i18n.t('admin_settings_load_failed'), 'error');
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
        if (!confirm(i18n.t('admin_settings_restart_confirm'))) return;
        var btn = document.getElementById('server-restart-btn');
        if (btn) btn.disabled = true;

        adminFetch('/api/server/restart', { method: 'POST' })
            .then(function (res) {
                if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_settings_restart_failed')); });
                showAdminToast(i18n.t('admin_settings_restarting'), 'success');
                setTimeout(function () { location.reload(); }, 3000);
            })
            .catch(function (err) {
                showAdminToast(err.message || i18n.t('admin_settings_restart_failed'), 'error');
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
            if (resultEl) { resultEl.textContent = i18n.t('admin_settings_smtp_test_empty'); resultEl.className = 'error-text'; resultEl.classList.remove('hidden'); }
            return;
        }

        // Send current form SMTP params so testing works before save
        var host = getVal('cfg-smtp-host');
        var port = parseInt(getVal('cfg-smtp-port')) || 0;
        var username = getVal('cfg-smtp-username');
        var password = getVal('cfg-smtp-password');
        var fromAddr = getVal('cfg-smtp-from-addr');
        var fromName = getVal('cfg-smtp-from-name');
        var tlsSelect = document.getElementById('cfg-smtp-tls');
        var useTLS = tlsSelect ? tlsSelect.value !== 'false' : true;
        var authMethodSelect = document.getElementById('cfg-smtp-auth-method');
        var authMethod = authMethodSelect ? authMethodSelect.value : 'PLAIN';

        if (btn) btn.disabled = true;
        if (resultEl) { resultEl.textContent = i18n.t('admin_settings_smtp_test_sending'); resultEl.className = ''; resultEl.classList.remove('hidden'); }

        adminFetch('/api/email/test', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: email, host: host, port: port, username: username, password: password, from_addr: fromAddr, from_name: fromName, use_tls: useTLS, auth_method: authMethod })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_settings_smtp_test_failed')); });
            return res.json();
        })
        .then(function () {
            if (resultEl) { resultEl.textContent = i18n.t('admin_settings_smtp_test_success'); resultEl.className = 'success-text'; }
        })
        .catch(function (err) {
            if (resultEl) { resultEl.textContent = err.message; resultEl.className = 'error-text'; }
        })
        .finally(function () {
            if (btn) btn.disabled = false;
        });
    };

    window.testLLM = function () {
        var btn = document.getElementById('btn-test-llm');
        var result = document.getElementById('test-llm-result');
        var spinner = document.getElementById('spinner-test-llm');
        var endpoint = getVal('cfg-llm-endpoint');
        var apiKey = getVal('cfg-llm-apikey');
        var model = getVal('cfg-llm-model');
        var temperature = parseFloat(getVal('cfg-llm-temperature')) || 0.3;
        var maxTokens = parseInt(getVal('cfg-llm-maxtokens')) || 64;

        // Allow empty apiKey — backend will fall back to saved config
        var apiKeyEl = document.getElementById('cfg-llm-apikey');
        var hasSavedKey = apiKeyEl && apiKeyEl.placeholder && apiKeyEl.placeholder.indexOf('***') !== -1;
        if (!endpoint || (!apiKey && !hasSavedKey) || !model) {
            if (result) { result.textContent = i18n.t('admin_settings_test_missing_fields'); result.style.color = '#e53e3e'; result.classList.remove('hidden'); }
            return;
        }
        if (btn) btn.disabled = true;
        if (spinner) spinner.classList.remove('hidden');
        if (result) { result.textContent = i18n.t('admin_settings_test_testing'); result.style.color = '#6b7280'; result.classList.remove('hidden'); }

        adminFetch('/api/test/llm', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ endpoint: endpoint, api_key: apiKey, model_name: model, temperature: temperature, max_tokens: maxTokens })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_settings_test_failed')); });
            return res.json();
        })
        .then(function (data) {
            if (result) { result.textContent = '✅ ' + i18n.t('admin_settings_test_success') + (data.reply ? ' — ' + data.reply : ''); result.style.color = '#38a169'; }
        })
        .catch(function (err) {
            if (result) { result.textContent = '❌ ' + (err.message || i18n.t('admin_settings_test_failed')); result.style.color = '#e53e3e'; }
        })
        .finally(function () {
            if (btn) btn.disabled = false;
            if (spinner) spinner.classList.add('hidden');
        });
    };

    window.testEmbedding = function () {
        var btn = document.getElementById('btn-test-embedding');
        var result = document.getElementById('test-embedding-result');
        var spinner = document.getElementById('spinner-test-embedding');
        var endpoint = getVal('cfg-emb-endpoint');
        var apiKey = getVal('cfg-emb-apikey');
        var model = getVal('cfg-emb-model');
        var multimodal = document.getElementById('cfg-emb-multimodal');
        var useMultimodal = multimodal ? multimodal.value === 'true' : false;

        // Allow empty apiKey — backend will fall back to saved config
        var apiKeyEl = document.getElementById('cfg-emb-apikey');
        var hasSavedKey = apiKeyEl && apiKeyEl.placeholder && apiKeyEl.placeholder.indexOf('***') !== -1;
        if (!endpoint || (!apiKey && !hasSavedKey) || !model) {
            if (result) { result.textContent = i18n.t('admin_settings_test_missing_fields'); result.style.color = '#e53e3e'; result.classList.remove('hidden'); }
            return;
        }
        if (btn) btn.disabled = true;
        if (spinner) spinner.classList.remove('hidden');
        if (result) { result.textContent = i18n.t('admin_settings_test_testing'); result.style.color = '#6b7280'; result.classList.remove('hidden'); }

        adminFetch('/api/test/embedding', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ endpoint: endpoint, api_key: apiKey, model_name: model, use_multimodal: useMultimodal })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_settings_test_failed')); });
            return res.json();
        })
        .then(function (data) {
            if (result) { result.textContent = '✅ ' + i18n.t('admin_settings_test_success') + ' — ' + (data.dimensions || 0) + ' dims'; result.style.color = '#38a169'; }
        })
        .catch(function (err) {
            if (result) { result.textContent = '❌ ' + (err.message || i18n.t('admin_settings_test_failed')); result.style.color = '#e53e3e'; }
        })
        .finally(function () {
            if (btn) btn.disabled = false;
            if (spinner) spinner.classList.add('hidden');
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
        var vecContentPriority = getVal('cfg-vec-content-priority');
        if (vecContentPriority) updates['vector.content_priority'] = vecContentPriority;
        var vecTextMatch = getVal('cfg-vec-text-match');
        updates['vector.text_match_enabled'] = vecTextMatch === 'true';
        var vecDebugMode = getVal('cfg-vec-debug-mode');
        updates['vector.debug_mode'] = vecDebugMode === 'true';

        var adminLoginRouteVal = getVal('cfg-admin-login-route');
        if (adminLoginRouteVal) {
            updates['admin.login_route'] = adminLoginRouteVal;
        }

        var productName = getVal('cfg-product-name');
        updates['product_name'] = productName;

        var productIntro = getVal('cfg-product-intro');
        updates['product_intro'] = productIntro;

        var authServer = getVal('cfg-auth-server');
        updates['auth_server'] = authServer;

        var smtpHost = getVal('cfg-smtp-host');
        var smtpPort = getVal('cfg-smtp-port');
        var smtpUsername = getVal('cfg-smtp-username');
        var smtpPassword = getVal('cfg-smtp-password');
        var smtpFromAddr = getVal('cfg-smtp-from-addr');
        var smtpFromName = getVal('cfg-smtp-from-name');
        var smtpTls = getVal('cfg-smtp-tls');
        var smtpAuthMethod = getVal('cfg-smtp-auth-method');

        if (smtpHost) updates['smtp.host'] = smtpHost;
        if (smtpPort !== '') updates['smtp.port'] = parseInt(smtpPort, 10);
        if (smtpUsername) updates['smtp.username'] = smtpUsername;
        if (smtpPassword) updates['smtp.password'] = smtpPassword;
        if (smtpFromAddr) updates['smtp.from_addr'] = smtpFromAddr;
        if (smtpFromName) updates['smtp.from_name'] = smtpFromName;
        updates['smtp.use_tls'] = smtpTls === 'true';
        updates['smtp.auth_method'] = smtpAuthMethod || '';

        // Collect OAuth provider settings
        var oauthCards = document.querySelectorAll('.oauth-provider-card');
        oauthCards.forEach(function (card) {
            var pName = card.getAttribute('data-provider');
            if (!pName) return;
            var cid = getVal('oauth-' + pName + '-client-id');
            var csecret = getVal('oauth-' + pName + '-client-secret');
            var aurl = getVal('oauth-' + pName + '-auth-url');
            var turl = getVal('oauth-' + pName + '-token-url');
            var rurl = getVal('oauth-' + pName + '-redirect-url');
            var scopes = getVal('oauth-' + pName + '-scopes');
            if (cid) updates['oauth.providers.' + pName + '.client_id'] = cid;
            if (csecret) updates['oauth.providers.' + pName + '.client_secret'] = csecret;
            if (aurl) updates['oauth.providers.' + pName + '.auth_url'] = aurl;
            if (turl) updates['oauth.providers.' + pName + '.token_url'] = turl;
            if (rurl) updates['oauth.providers.' + pName + '.redirect_url'] = rurl;
            if (scopes) updates['oauth.providers.' + pName + '.scopes'] = scopes;
        });

        if (Object.keys(updates).length === 0) {
            showAdminToast(i18n.t('admin_settings_no_changes'), 'info');
            return;
        }

        adminFetch('/api/config', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(updates)
        })
        .then(function (res) {
            if (!res.ok) throw new Error(i18n.t('admin_settings_save_failed'));
            showAdminToast(i18n.t('admin_settings_saved'), 'success');
            loadAdminSettings();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_settings_save_failed'), 'error');
        });
    };

    // --- Log Management ---

    window.loadRecentLogs = function () {
        var content = document.getElementById('log-viewer-content');
        if (!content) return;
        content.textContent = '加载中...';
        adminFetch('/api/logs/recent?lines=50')
            .then(function (res) {
                if (!res.ok) throw new Error('加载失败');
                return res.json();
            })
            .then(function (data) {
                var lines = data.lines || [];
                var rotMB = data.rotation_mb || 100;
                var rotInput = document.getElementById('cfg-log-rotation-mb');
                if (rotInput) rotInput.value = rotMB;
                if (lines.length === 0) {
                    content.innerHTML = '<span style="color:#94a3b8;">暂无日志记录</span>';
                    return;
                }
                // Render lines with color coding
                var html = '';
                lines.forEach(function (line) {
                    var escaped = line.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
                    // Highlight timestamp
                    escaped = escaped.replace(/^(\d{4}\/\d{2}\/\d{2}\s+\d{2}:\d{2}:\d{2})/, '<span class="log-line-time">$1</span>');
                    // Highlight [ERROR]
                    escaped = escaped.replace(/\[ERROR\]/, '<span class="log-line-error">[ERROR]</span>');
                    html += escaped + '\n';
                });
                content.innerHTML = html;
                // Auto-scroll to bottom
                var viewer = document.getElementById('log-viewer');
                if (viewer) viewer.scrollTop = viewer.scrollHeight;
            })
            .catch(function (err) {
                content.textContent = '加载日志失败: ' + (err.message || '未知错误');
            });
    };

    window.saveLogRotation = function () {
        var input = document.getElementById('cfg-log-rotation-mb');
        if (!input) return;
        var val = parseInt(input.value, 10);
        if (isNaN(val) || val < 1 || val > 10240) {
            showAdminToast('轮转大小必须在 1-10240 MB 之间', 'error');
            return;
        }
        adminFetch('/api/logs/rotation', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ rotation_mb: val })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || '保存失败'); });
            showAdminToast('日志轮转大小已保存', 'success');
        })
        .catch(function (err) {
            showAdminToast(err.message || '保存失败', 'error');
        });
    };

    window.downloadLogs = function () {
        var token = localStorage.getItem('admin_session');
        if (!token) {
            showAdminToast('请先登录', 'error');
            return;
        }
        // Use fetch to download with auth header, then trigger browser download
        fetch('/api/logs/download', {
            headers: { 'Authorization': 'Bearer ' + token }
        })
        .then(function (res) {
            if (!res.ok) throw new Error('下载失败');
            return res.blob();
        })
        .then(function (blob) {
            var url = URL.createObjectURL(blob);
            var a = document.createElement('a');
            a.href = url;
            a.download = 'error_log.gz';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
            showAdminToast('日志下载完成', 'success');
        })
        .catch(function (err) {
            showAdminToast(err.message || '下载失败', 'error');
        });
    };

    // --- Multimodal Settings ---

    function loadMultimodalSettings() {
        adminFetch('/api/config')
            .then(function (res) {
                if (!res.ok) throw new Error('load failed');
                return res.json();
            })
            .then(function (cfg) {
                var video = cfg.video || {};
                setVal('cfg-video-ffmpeg-path', video.ffmpeg_path || '');
                setVal('cfg-video-rapidspeech-path', video.rapidspeech_path || '');
                setVal('cfg-video-keyframe-interval', video.keyframe_interval || 10);
                setVal('cfg-video-rapidspeech-model', video.rapidspeech_model || '');
                setVal('cfg-video-max-upload-size', video.max_upload_size_mb || 500);
                checkMultimodalDeps();
            })
            .catch(function () {
                showAdminToast(i18n.t('admin_settings_load_failed'), 'error');
            });
    }

    window.checkMultimodalDeps = function () {
        var ffmpegIcon = document.getElementById('dep-ffmpeg-icon');
        var ffmpegLabel = document.getElementById('dep-ffmpeg-label');
        var rapidspeechIcon = document.getElementById('dep-rapidspeech-icon');
        var rapidspeechLabel = document.getElementById('dep-rapidspeech-label');
        var ffmpegDetail = document.getElementById('dep-ffmpeg-detail');
        var rapidspeechDetail = document.getElementById('dep-rapidspeech-detail');
        if (ffmpegIcon) ffmpegIcon.textContent = '⏳';
        if (ffmpegLabel) ffmpegLabel.textContent = i18n.t('admin_multimodal_checking');
        if (rapidspeechIcon) rapidspeechIcon.textContent = '⏳';
        if (rapidspeechLabel) rapidspeechLabel.textContent = i18n.t('admin_multimodal_checking');
        if (ffmpegDetail) { ffmpegDetail.textContent = ''; ffmpegDetail.style.display = 'none'; }
        if (rapidspeechDetail) { rapidspeechDetail.textContent = ''; rapidspeechDetail.style.display = 'none'; }

        adminFetch('/api/video/check-deps')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (ffmpegIcon) ffmpegIcon.textContent = data.ffmpeg_ok ? '✅' : '❌';
                if (ffmpegLabel) {
                    ffmpegLabel.textContent = data.ffmpeg_ok ? i18n.t('admin_multimodal_available') : i18n.t('admin_multimodal_not_found');
                    ffmpegLabel.style.color = data.ffmpeg_ok ? '#38a169' : '#e53e3e';
                }
                if (ffmpegDetail) {
                    if (!data.ffmpeg_ok && data.ffmpeg_error) {
                        ffmpegDetail.textContent = data.ffmpeg_error;
                        ffmpegDetail.style.display = 'block';
                    } else {
                        ffmpegDetail.textContent = '';
                        ffmpegDetail.style.display = 'none';
                    }
                }
                if (rapidspeechIcon) rapidspeechIcon.textContent = data.rapidspeech_ok ? '✅' : '❌';
                if (rapidspeechLabel) {
                    rapidspeechLabel.textContent = data.rapidspeech_ok ? i18n.t('admin_multimodal_available') : i18n.t('admin_multimodal_not_found');
                    rapidspeechLabel.style.color = data.rapidspeech_ok ? '#38a169' : '#e53e3e';
                }
                if (rapidspeechDetail) {
                    if (!data.rapidspeech_ok && data.rapidspeech_error) {
                        rapidspeechDetail.textContent = data.rapidspeech_error;
                        rapidspeechDetail.style.display = 'block';
                    } else {
                        rapidspeechDetail.textContent = '';
                        rapidspeechDetail.style.display = 'none';
                    }
                }
            })
            .catch(function () {
                if (ffmpegIcon) ffmpegIcon.textContent = '❌';
                if (ffmpegLabel) ffmpegLabel.textContent = i18n.t('admin_multimodal_check_failed');
                if (rapidspeechIcon) rapidspeechIcon.textContent = '❌';
                if (rapidspeechLabel) rapidspeechLabel.textContent = i18n.t('admin_multimodal_check_failed');
            });
    };

    window.saveMultimodalSettings = function () {
        var updates = {};
        var ffmpegPath = getVal('cfg-video-ffmpeg-path');
        var rapidspeechPath = getVal('cfg-video-rapidspeech-path');
        var keyframeInterval = getVal('cfg-video-keyframe-interval');
        var rapidspeechModel = getVal('cfg-video-rapidspeech-model');
        var maxUploadSize = getVal('cfg-video-max-upload-size');

        updates['video.ffmpeg_path'] = ffmpegPath;
        updates['video.rapidspeech_path'] = rapidspeechPath;
        if (keyframeInterval !== '') updates['video.keyframe_interval'] = parseInt(keyframeInterval, 10);
        if (rapidspeechModel) updates['video.rapidspeech_model'] = rapidspeechModel;
        if (maxUploadSize !== '') updates['video.max_upload_size_mb'] = parseInt(maxUploadSize, 10);

        // Pre-save validation for RapidSpeech paths
        var needsValidation = rapidspeechPath || rapidspeechModel;
        var doSave = function () {
            adminFetch('/api/config', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(updates)
            })
            .then(function (res) {
                if (!res.ok) return res.json().then(function (data) { throw new Error(data.error || i18n.t('admin_settings_save_failed')); });
                showAdminToast(i18n.t('admin_settings_saved'), 'success');
                loadMultimodalSettings();
            })
            .catch(function (err) {
                showAdminToast(err.message || i18n.t('admin_settings_save_failed'), 'error');
            });
        };

        if (needsValidation) {
            adminFetch('/api/video/validate-rapidspeech', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    rapidspeech_path: rapidspeechPath,
                    rapidspeech_model: rapidspeechModel
                })
            })
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (data.valid) {
                    doSave();
                } else {
                    var errMsg = (data.errors || []).join('\n');
                    showAdminToast(errMsg || i18n.t('admin_settings_save_failed'), 'error');
                }
            })
            .catch(function () {
                showAdminToast(i18n.t('admin_multimodal_validate_failed'), 'error');
            });
        } else {
            doSave();
        }
    };

    // --- Auto Setup for Multimodal Dependencies ---

    var autoSetupRunning = false;
    var autoSetupAbort = null;

    window.startAutoSetup = function () {
        var modal = document.getElementById('auto-setup-modal');
        if (modal) {
            modal.classList.remove('hidden');
            var log = document.getElementById('auto-setup-log');
            if (log) log.innerHTML = '';
            var fill = document.getElementById('auto-setup-progress-fill');
            if (fill) fill.style.width = '0%';
            var text = document.getElementById('auto-setup-progress-text');
            if (text) text.textContent = '0%';
            var status = document.getElementById('auto-setup-status');
            if (status) {
                status.textContent = i18n.t('admin_multimodal_auto_setup_ready');
                status.className = 'auto-setup-status';
            }
            var startBtn = document.getElementById('auto-setup-start-btn');
            if (startBtn) { startBtn.disabled = false; startBtn.classList.remove('hidden'); }
            var closeBtn = document.getElementById('auto-setup-close-btn');
            if (closeBtn) closeBtn.disabled = false;
        }
    };

    window.closeAutoSetup = function () {
        var wasRunning = autoSetupRunning;
        if (autoSetupRunning) {
            if (!confirm(i18n.t('admin_multimodal_auto_setup_cancel_confirm'))) return;
            if (autoSetupAbort) { autoSetupAbort.abort(); autoSetupAbort = null; }
            autoSetupRunning = false;
        }
        var modal = document.getElementById('auto-setup-modal');
        if (modal) modal.classList.add('hidden');
        if (wasRunning) {
            checkMultimodalDeps();
            loadMultimodalSettings();
        }
    };

    window.confirmAutoSetup = function () {
        if (autoSetupRunning) return;
        autoSetupRunning = true;
        autoSetupAbort = new AbortController();

        var startBtn = document.getElementById('auto-setup-start-btn');
        if (startBtn) startBtn.classList.add('hidden');
        var closeBtn = document.getElementById('auto-setup-close-btn');
        if (closeBtn) closeBtn.disabled = true;

        var log = document.getElementById('auto-setup-log');
        var fill = document.getElementById('auto-setup-progress-fill');
        var text = document.getElementById('auto-setup-progress-text');
        var status = document.getElementById('auto-setup-status');

        // Batch DOM writes via requestAnimationFrame to avoid layout thrashing
        var pendingLogs = [];
        var logFlushScheduled = false;
        function flushLogs() {
            logFlushScheduled = false;
            if (!log || pendingLogs.length === 0) return;
            var frag = document.createDocumentFragment();
            for (var j = 0; j < pendingLogs.length; j++) {
                var item = pendingLogs[j];
                var div = document.createElement('div');
                if (item.cls) div.className = item.cls;
                div.textContent = item.msg;
                frag.appendChild(div);
            }
            pendingLogs = [];
            log.appendChild(frag);
            log.scrollTop = log.scrollHeight;
        }

        function appendLog(msg, cls) {
            pendingLogs.push({ msg: msg, cls: cls || '' });
            if (!logFlushScheduled) {
                logFlushScheduled = true;
                requestAnimationFrame(flushLogs);
            }
        }

        function setProgress(pct) {
            if (pct < 0) return;
            if (fill) fill.style.width = pct + '%';
            if (text) text.textContent = pct + '%';
        }

        if (status) {
            status.textContent = i18n.t('admin_multimodal_auto_setup_running');
            status.className = 'auto-setup-status';
        }

        fetch('/api/video/auto-setup', {
            method: 'POST',
            headers: { 'Authorization': 'Bearer ' + getAdminToken() },
            signal: autoSetupAbort.signal
        }).then(function (response) {
            if (!response.ok) {
                return response.json().then(function (data) {
                    throw new Error(data.error || i18n.t('admin_multimodal_auto_setup_failed'));
                });
            }
            var reader = response.body.getReader();
            var decoder = new TextDecoder();
            var buffer = '';

            function processChunk() {
                return reader.read().then(function (result) {
                    if (result.done) {
                        flushLogs();
                        autoSetupRunning = false;
                        autoSetupAbort = null;
                        if (closeBtn) closeBtn.disabled = false;
                        return;
                    }
                    buffer += decoder.decode(result.value, { stream: true });
                    var lines = buffer.split('\n');
                    buffer = lines.pop() || '';

                    for (var i = 0; i < lines.length; i++) {
                        var line = lines[i].trim();
                        if (line.indexOf('data: ') !== 0) continue;
                        try {
                            var evt = JSON.parse(line.substring(6));
                            if (evt.progress >= 0) setProgress(evt.progress);

                            if (evt.type === 'step') {
                                appendLog(evt.message, 'log-step');
                                if (status) { status.textContent = evt.message; status.className = 'auto-setup-status'; }
                            } else if (evt.type === 'error') {
                                appendLog('❌ ' + evt.message, 'log-error');
                                if (status) { status.textContent = evt.message; status.className = 'auto-setup-status error'; }
                            } else if (evt.type === 'done') {
                                flushLogs();
                                var isSuccess = evt.progress === 100;
                                if (isSuccess) {
                                    appendLog('✅ ' + evt.message, 'log-success');
                                    if (status) { status.textContent = evt.message; status.className = 'auto-setup-status success'; }
                                } else {
                                    if (status) { status.className = 'auto-setup-status error'; }
                                }
                                autoSetupRunning = false;
                                autoSetupAbort = null;
                                if (closeBtn) closeBtn.disabled = false;
                                if (isSuccess) { checkMultimodalDeps(); loadMultimodalSettings(); }
                            } else if (evt.type === 'log') {
                                appendLog(evt.message, '');
                            }
                        } catch (e) { /* ignore parse errors */ }
                    }
                    return processChunk();
                });
            }
            return processChunk();
        }).catch(function (err) {
            flushLogs();
            if (err.name === 'AbortError') {
                appendLog('⚠️ ' + i18n.t('admin_multimodal_auto_setup_aborted'), 'log-error');
                if (status) { status.textContent = i18n.t('admin_multimodal_auto_setup_aborted'); status.className = 'auto-setup-status error'; }
            } else {
                appendLog('❌ ' + (err.message || i18n.t('admin_multimodal_auto_setup_conn_failed')), 'log-error');
                if (status) { status.textContent = err.message || i18n.t('admin_multimodal_auto_setup_conn_failed'); status.className = 'auto-setup-status error'; }
            }
            autoSetupRunning = false;
            autoSetupAbort = null;
            if (closeBtn) closeBtn.disabled = false;
        });
    };

    // --- OAuth Admin Settings ---

    var oauthDefaultConfigs = {
        google: {
            auth_url: 'https://accounts.google.com/o/oauth2/v2/auth',
            token_url: 'https://oauth2.googleapis.com/token',
            scopes: 'openid,email,profile'
        },
        apple: {
            auth_url: 'https://appleid.apple.com/auth/authorize',
            token_url: 'https://appleid.apple.com/auth/token',
            scopes: 'name,email'
        },
        amazon: {
            auth_url: 'https://www.amazon.com/ap/oa',
            token_url: 'https://api.amazon.com/auth/o2/token',
            scopes: 'profile'
        },
        facebook: {
            auth_url: 'https://www.facebook.com/v18.0/dialog/oauth',
            token_url: 'https://graph.facebook.com/v18.0/oauth/access_token',
            scopes: 'email,public_profile'
        }
    };

    var oauthProviderLabels = {
        google: 'Google',
        apple: 'Apple',
        amazon: 'Amazon',
        facebook: 'Facebook'
    };

    function renderOAuthProviderSettings(oauthCfg) {
        var container = document.getElementById('oauth-providers-list');
        if (!container) return;
        container.innerHTML = '';
        var providers = (oauthCfg && oauthCfg.providers) ? oauthCfg.providers : {};
        var names = Object.keys(providers);
        if (names.length === 0) {
            container.innerHTML = '<p class="admin-table-empty" data-i18n="admin_settings_oauth_empty">' + i18n.t('admin_settings_oauth_empty') + '</p>';
            return;
        }
        names.forEach(function (name) {
            var p = providers[name];
            var label = oauthProviderLabels[name] || name;
            var isConfigured = p.client_id && p.client_secret && p.auth_url && p.token_url;
            var statusClass = isConfigured ? 'oauth-status-ok' : 'oauth-status-incomplete';
            var statusText = isConfigured ? i18n.t('admin_settings_oauth_status_ok') : i18n.t('admin_settings_oauth_status_incomplete');

            var card = document.createElement('div');
            card.className = 'oauth-provider-card';
            card.setAttribute('data-provider', name);
            card.innerHTML =
                '<div class="oauth-provider-header">' +
                    '<span class="oauth-provider-name">' + label + '</span>' +
                    '<span class="oauth-status ' + statusClass + '">' + statusText + '</span>' +
                    '<button class="btn-danger btn-sm" onclick="removeOAuthProvider(\'' + name + '\')">' + i18n.t('admin_settings_oauth_remove') + '</button>' +
                '</div>' +
                '<div class="oauth-provider-fields">' +
                    '<div class="admin-form-row"><label>Client ID</label><input type="text" id="oauth-' + name + '-client-id" value="' + escapeAttr(p.client_id || '') + '" placeholder="Client ID"></div>' +
                    '<div class="admin-form-row"><label>Client Secret</label><input type="password" id="oauth-' + name + '-client-secret" value="" placeholder="' + (p.client_secret ? '***' : 'Client Secret') + '"></div>' +
                    '<div class="admin-form-row"><label>Auth URL</label><input type="text" id="oauth-' + name + '-auth-url" value="' + escapeAttr(p.auth_url || '') + '" placeholder="Authorization URL"></div>' +
                    '<div class="admin-form-row"><label>Token URL</label><input type="text" id="oauth-' + name + '-token-url" value="' + escapeAttr(p.token_url || '') + '" placeholder="Token URL"></div>' +
                    '<div class="admin-form-row"><label>Redirect URL</label><input type="text" id="oauth-' + name + '-redirect-url" value="' + escapeAttr(p.redirect_url || '') + '" placeholder="' + window.location.origin + '/oauth/callback"></div>' +
                    '<div class="admin-form-row"><label>Scopes</label><input type="text" id="oauth-' + name + '-scopes" value="' + escapeAttr((p.scopes || []).join(',')) + '" placeholder="openid,email,profile"></div>' +
                '</div>';
            container.appendChild(card);
        });
    }

    function escapeAttr(s) {
        return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    }

    window.addOAuthProviderForm = function () {
        var select = document.getElementById('oauth-add-provider-select');
        if (!select) return;
        var name = select.value;
        // Check if already exists
        if (document.querySelector('.oauth-provider-card[data-provider="' + name + '"]')) {
            showAdminToast(i18n.t('admin_settings_oauth_exists'), 'info');
            return;
        }
        var defaults = oauthDefaultConfigs[name] || {};
        var fakeOAuth = { providers: {} };
        fakeOAuth.providers[name] = {
            client_id: '',
            client_secret: '',
            auth_url: defaults.auth_url || '',
            token_url: defaults.token_url || '',
            redirect_url: window.location.origin + '/oauth/callback',
            scopes: (defaults.scopes || '').split(',')
        };
        // Append to existing
        var container = document.getElementById('oauth-providers-list');
        var emptyMsg = container.querySelector('.admin-table-empty');
        if (emptyMsg) emptyMsg.remove();
        var tempDiv = document.createElement('div');
        tempDiv.innerHTML = '';
        var p = fakeOAuth.providers[name];
        var label = oauthProviderLabels[name] || name;
        var card = document.createElement('div');
        card.className = 'oauth-provider-card';
        card.setAttribute('data-provider', name);
        card.innerHTML =
            '<div class="oauth-provider-header">' +
                '<span class="oauth-provider-name">' + label + '</span>' +
                '<span class="oauth-status oauth-status-incomplete">' + i18n.t('admin_settings_oauth_status_incomplete') + '</span>' +
                '<button class="btn-danger btn-sm" onclick="removeOAuthProvider(\'' + name + '\')">' + i18n.t('admin_settings_oauth_remove') + '</button>' +
            '</div>' +
            '<div class="oauth-provider-fields">' +
                '<div class="admin-form-row"><label>Client ID</label><input type="text" id="oauth-' + name + '-client-id" value="" placeholder="Client ID"></div>' +
                '<div class="admin-form-row"><label>Client Secret</label><input type="password" id="oauth-' + name + '-client-secret" value="" placeholder="Client Secret"></div>' +
                '<div class="admin-form-row"><label>Auth URL</label><input type="text" id="oauth-' + name + '-auth-url" value="' + escapeAttr(p.auth_url || '') + '" placeholder="Authorization URL"></div>' +
                '<div class="admin-form-row"><label>Token URL</label><input type="text" id="oauth-' + name + '-token-url" value="' + escapeAttr(p.token_url || '') + '" placeholder="Token URL"></div>' +
                '<div class="admin-form-row"><label>Redirect URL</label><input type="text" id="oauth-' + name + '-redirect-url" value="' + escapeAttr(p.redirect_url || '') + '" placeholder="' + window.location.origin + '/oauth/callback"></div>' +
                '<div class="admin-form-row"><label>Scopes</label><input type="text" id="oauth-' + name + '-scopes" value="' + escapeAttr((p.scopes || []).join(',')) + '" placeholder="openid,email,profile"></div>' +
            '</div>';
        container.appendChild(card);
        showAdminToast(i18n.t('admin_settings_oauth_added') + ' ' + label, 'success');
    };

    window.removeOAuthProvider = function (name) {
        if (!confirm(i18n.t('admin_settings_oauth_remove_confirm'))) return;
        adminFetch('/api/oauth/providers/' + name, { method: 'DELETE' })
            .then(function (res) {
                if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || 'Failed'); });
                var card = document.querySelector('.oauth-provider-card[data-provider="' + name + '"]');
                if (card) card.remove();
                showAdminToast(i18n.t('admin_settings_oauth_removed'), 'success');
                // If no more cards, show empty message
                var container = document.getElementById('oauth-providers-list');
                if (container && !container.querySelector('.oauth-provider-card')) {
                    container.innerHTML = '<p class="admin-table-empty">' + i18n.t('admin_settings_oauth_empty') + '</p>';
                }
            })
            .catch(function (err) {
                showAdminToast(err.message, 'error');
            });
    };

    // --- OAuth Login Buttons ---

    var oauthProviderIcons = {
        google: '<svg class="oauth-icon" width="20" height="20" viewBox="0 0 24 24"><path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 01-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4"/><path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853"/><path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#FBBC05"/><path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335"/></svg>',
        apple: '<svg class="oauth-icon" width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M17.05 20.28c-.98.95-2.05.88-3.08.4-1.09-.5-2.08-.48-3.24 0-1.44.62-2.2.44-3.06-.4C2.79 15.25 3.51 7.59 9.05 7.31c1.35.07 2.29.74 3.08.8 1.18-.24 2.31-.93 3.57-.84 1.51.12 2.65.72 3.4 1.8-3.12 1.87-2.38 5.98.48 7.13-.57 1.5-1.31 2.99-2.54 4.09zM12.03 7.25c-.15-2.23 1.66-4.07 3.74-4.25.29 2.58-2.34 4.5-3.74 4.25z"/></svg>',
        amazon: '<svg class="oauth-icon" width="20" height="20" viewBox="0 0 24 24"><path d="M13.96 22.45c3.75-2.76 5.58-5.57 5.58-8.45 0-1.8-.72-2.7-2.16-2.7-.96 0-1.68.54-2.16 1.62l-.48-.3c.6-1.44 1.68-2.16 3.24-2.16 1.92 0 2.88 1.14 2.88 3.42 0 3.12-2.04 6.12-6.06 9l-.84-.43z" fill="#FF9900"/><path d="M1.2 17.55C4.08 19.85 7.68 21 12 21c2.76 0 5.52-.66 8.28-1.98l.48.84C17.64 21.62 14.76 22.5 12 22.5c-4.56 0-8.28-1.26-11.16-3.78l.36-1.17z" fill="#FF9900"/><path d="M6.96 12.75c0-1.56.42-2.82 1.26-3.78.84-.96 1.92-1.44 3.24-1.44 1.2 0 2.16.42 2.88 1.26v-1.02h1.8v7.38c0 1.68-.48 2.94-1.44 3.78-.96.84-2.22 1.26-3.78 1.26-1.32 0-2.52-.36-3.6-1.08l.72-1.44c.84.6 1.8.9 2.88.9 1.92 0 2.88-1.02 2.88-3.06v-.72c-.72.84-1.68 1.26-2.88 1.26-1.2 0-2.22-.48-3.06-1.44-.84-.96-1.26-2.16-1.26-3.6zm1.8.12c0 1.08.3 1.98.9 2.7.6.72 1.38 1.08 2.34 1.08.96 0 1.74-.36 2.34-1.08V9.93c-.6-.72-1.38-1.08-2.34-1.08-.96 0-1.74.36-2.34 1.08-.6.72-.9 1.62-.9 2.7z" fill="#232F3E"/></svg>',
        facebook: '<svg class="oauth-icon" width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M24 12.073c0-6.627-5.373-12-12-12s-12 5.373-12 12c0 5.99 4.388 10.954 10.125 11.854v-8.385H7.078v-3.47h3.047V9.43c0-3.007 1.792-4.669 4.533-4.669 1.312 0 2.686.235 2.686.235v2.953H15.83c-1.491 0-1.956.925-1.956 1.874v2.25h3.328l-.532 3.47h-2.796v8.385C19.612 23.027 24 18.062 24 12.073z"/></svg>'
    };

    function renderOAuthLoginButtons(providers) {
        var container = document.getElementById('oauth-login-buttons');
        var divider = document.getElementById('oauth-login-divider');
        if (!container) return;
        if (!providers || providers.length === 0) {
            container.classList.add('hidden');
            if (divider) divider.classList.add('hidden');
            return;
        }
        container.innerHTML = '';
        providers.forEach(function (name) {
            var label = oauthProviderLabels[name] || name;
            var icon = oauthProviderIcons[name] || '';
            var btn = document.createElement('button');
            btn.className = 'oauth-btn oauth-' + name;
            btn.innerHTML = icon + '<span>' + i18n.t('login_oauth_with') + ' ' + label + '</span>';
            btn.onclick = function () { startOAuthLogin(name); };
            container.appendChild(btn);
        });
        container.classList.remove('hidden');
        if (divider) divider.classList.remove('hidden');
    }

    function startOAuthLogin(provider) {
        fetch('/api/oauth/url?provider=' + encodeURIComponent(provider))
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (data.url) {
                    window.location.href = data.url;
                } else {
                    showToast(i18n.t('login_oauth_failed'), 'error');
                }
            })
            .catch(function () {
                showToast(i18n.t('login_oauth_failed'), 'error');
            });
    }

    // Handle OAuth callback (when redirected back from provider)
    function handleOAuthCallbackFromURL() {
        var params = new URLSearchParams(window.location.search);
        var code = params.get('code');
        var state = params.get('state');
        if (!code || !state) return false;
        // Extract provider from state (format: "state-{provider}")
        var provider = state.replace('state-', '');
        if (!provider) return false;

        fetch('/api/oauth/callback', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ provider: provider, code: code })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || 'OAuth failed'); });
            return res.json();
        })
        .then(function (data) {
            if (data.session && data.user) {
                saveSession(data.session, { id: data.user.id, email: data.user.email, name: data.user.name, provider: data.user.provider });
                window.history.replaceState({}, '', '/chat');
                handleRoute();
            }
        })
        .catch(function (err) {
            showToast(err.message || i18n.t('login_oauth_failed'), 'error');
            window.history.replaceState({}, '', '/login');
            handleRoute();
        });
        return true;
    }

    // Handle ticket-login callback (SN login via desktop app)
    // When /auth/ticket-login redirects to /?ticket=xxx, this function
    // exchanges the ticket for a session via the backend API.
    function handleTicketLoginFromURL() {
        var params = new URLSearchParams(window.location.search);
        var ticket = params.get('ticket');
        if (!ticket) return false;

        // Clean the URL immediately so the ticket isn't visible/reusable
        window.history.replaceState({}, '', '/');

        fetch('/api/auth/ticket-exchange', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ticket: ticket })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.message || 'ticket login failed'); });
            return res.json();
        })
        .then(function (data) {
            if (data.session && data.user) {
                saveSession(data.session, { id: data.user.id, email: data.user.email, name: data.user.name, provider: data.user.provider });
                fetchProducts();
                window.history.replaceState({}, '', '/chat');
                handleRoute();
            }
        })
        .catch(function (err) {
            window.history.replaceState({}, '', '/login?error=ticket_failed');
            handleRoute();
        });
        return true;
    }

    // --- Knowledge Entry ---

    var knowledgeImageURLs = [];
    var knowledgeVideoURLs = [];

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
            showAdminToast(i18n.t('image_select_error'), 'error');
            return;
        }
        if (file.size > 10 * 1024 * 1024) {
            showAdminToast(i18n.t('image_size_error'), 'error');
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

        // Add progress bar overlay on this image item
        var progressBar = addProgressOverlay(item, false);

        var formData = new FormData();
        formData.append('image', file, file.name || 'paste.png');

        var token = getAdminToken();
        var xhr = new XMLHttpRequest();
        xhr.open('POST', '/api/images/upload', true);
        xhr.setRequestHeader('Authorization', 'Bearer ' + token);

        xhr.upload.onprogress = function (e) {
            if (e.lengthComputable && progressBar) {
                setProgressBar(progressBar, (e.loaded / e.total) * 90);
            }
        };

        xhr.onload = function () {
            if (progressBar) setProgressBar(progressBar, 100);
            setTimeout(function () { removeProgressOverlay(item); }, 300);
            item.classList.remove('uploading');

            if (xhr.status >= 200 && xhr.status < 300) {
                var data;
                try { data = JSON.parse(xhr.responseText); } catch (e) { data = {}; }
                var idx = knowledgeImageURLs.length;
                knowledgeImageURLs.push(data.url);

                // Add remove button
                var removeBtn = document.createElement('button');
                removeBtn.className = 'knowledge-image-remove';
                removeBtn.textContent = '×';
                removeBtn.setAttribute('aria-label', i18n.t('image_remove_label'));
                removeBtn.onclick = function () {
                    knowledgeImageURLs[idx] = null;
                    item.remove();
                };
                item.appendChild(removeBtn);
            } else {
                item.remove();
                showAdminToast(i18n.t('image_upload_failed'), 'error');
            }
        };

        xhr.onerror = function () {
            item.remove();
            showAdminToast(i18n.t('image_upload_failed'), 'error');
        };

        xhr.send(formData);
    }

    function initKnowledgeVideoZone() {
        var area = document.getElementById('knowledge-video-upload-area');
        var input = document.getElementById('knowledge-video-input');
        if (!area || !input) return;

        // Click to select files
        area.addEventListener('click', function () {
            input.click();
        });

        // File input change
        input.addEventListener('change', function () {
            if (input.files && input.files.length > 0) {
                for (var i = 0; i < input.files.length; i++) {
                    uploadKnowledgeVideo(input.files[i]);
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
                if (files[i].type.indexOf('video/') === 0) {
                    uploadKnowledgeVideo(files[i]);
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
                    if (items[i].type.indexOf('video/') === 0) {
                        e.preventDefault();
                        var blob = items[i].getAsFile();
                        if (blob) uploadKnowledgeVideo(blob);
                    }
                }
            });
        }
    }

    function uploadKnowledgeVideo(file) {
        if (file.type.indexOf('video/') !== 0) {
            showAdminToast(i18n.t('video_select_error'), 'error');
            return;
        }
        if (file.size > maxUploadSizeMB * 1024 * 1024) {
            showAdminToast(i18n.t('video_size_error', { size: maxUploadSizeMB }), 'error');
            return;
        }

        // Create preview placeholder
        var preview = document.getElementById('knowledge-video-preview');
        var item = document.createElement('div');
        item.className = 'knowledge-video-item uploading';

        var video = document.createElement('video');
        video.src = URL.createObjectURL(file);
        video.controls = true;
        video.muted = true;
        item.appendChild(video);
        preview.appendChild(item);

        // Add progress bar overlay on this video item
        var progressBar = addProgressOverlay(item, false);

        var formData = new FormData();
        formData.append('video', file, file.name || 'paste.mp4');

        var token = getAdminToken();
        var xhr = new XMLHttpRequest();
        xhr.open('POST', '/api/videos/upload', true);
        xhr.setRequestHeader('Authorization', 'Bearer ' + token);

        xhr.upload.onprogress = function (e) {
            if (e.lengthComputable && progressBar) {
                setProgressBar(progressBar, (e.loaded / e.total) * 90);
            }
        };

        xhr.onload = function () {
            if (progressBar) setProgressBar(progressBar, 100);
            setTimeout(function () { removeProgressOverlay(item); }, 300);
            item.classList.remove('uploading');

            if (xhr.status >= 200 && xhr.status < 300) {
                var data;
                try { data = JSON.parse(xhr.responseText); } catch (e) { data = {}; }
                var idx = knowledgeVideoURLs.length;
                knowledgeVideoURLs.push(data.url);

                // Add remove button
                var removeBtn = document.createElement('button');
                removeBtn.className = 'knowledge-video-remove';
                removeBtn.textContent = '×';
                removeBtn.setAttribute('aria-label', i18n.t('video_remove_label'));
                removeBtn.onclick = function () {
                    knowledgeVideoURLs[idx] = null;
                    item.remove();
                };
                item.appendChild(removeBtn);
            } else {
                item.remove();
                showAdminToast(i18n.t('video_upload_failed'), 'error');
            }
        };

        xhr.onerror = function () {
            item.remove();
            showAdminToast(i18n.t('video_upload_failed'), 'error');
        };

        xhr.send(formData);
    }

    window.submitKnowledgeEntry = function () {
        var title = (document.getElementById('knowledge-title') || {}).value || '';
        var content = (document.getElementById('knowledge-content') || {}).value || '';

        if (!title.trim() || !content.trim()) {
            showAdminToast(i18n.t('admin_knowledge_empty'), 'error');
            return;
        }

        var imageURLs = knowledgeImageURLs.filter(function (u) { return u; });
        var videoURLs = knowledgeVideoURLs.filter(function (u) { return u; });

        var btn = document.getElementById('knowledge-submit-btn');
        setBtnLoading(btn, i18n.t('admin_knowledge_submitting_btn'));
        showAdminToast(i18n.t('admin_knowledge_submitting'), 'info');

        // Show indeterminate progress on knowledge image zone if images present
        var imgZone = document.getElementById('knowledge-image-zone');
        if (imageURLs.length > 0 && imgZone) {
            addProgressOverlay(imgZone, true, 'knowledge-submit-progress');
            imgZone.style.position = 'relative';
        }

        adminFetch('/api/knowledge', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ title: title.trim(), content: content.trim(), image_urls: imageURLs, video_urls: videoURLs, product_id: getKnowledgeProductID() })
        })
        .then(function (res) {
            if (!res.ok) {
                return res.text().then(function (text) {
                    var msg = i18n.t('admin_knowledge_failed');
                    try { var d = JSON.parse(text); msg = d.error || msg; } catch (e) { /* non-JSON response */ }
                    throw new Error(msg);
                });
            }
            return res.text().then(function (text) {
                try { return JSON.parse(text); } catch (e) { return {}; }
            });
        })
        .then(function () {
            showAdminToast(i18n.t('admin_knowledge_success'), 'success');
            if (document.getElementById('knowledge-title')) document.getElementById('knowledge-title').value = '';
            if (document.getElementById('knowledge-content')) document.getElementById('knowledge-content').value = '';
            var preview = document.getElementById('knowledge-image-preview');
            if (preview) preview.innerHTML = '';
            knowledgeImageURLs = [];
            var videoPreview = document.getElementById('knowledge-video-preview');
            if (videoPreview) videoPreview.innerHTML = '';
            knowledgeVideoURLs = [];
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_knowledge_failed'), 'error');
        })
        .finally(function () {
            resetBtnLoading(btn);
            if (imgZone) removeProgressOverlay(imgZone, 'knowledge-submit-progress');
        });
    };

    // --- Product Management ---

    function loadProducts() {
        // Manually apply i18n to product tab elements that may not get translated
        var dlLabel = document.getElementById('product-allow-download-label');
        var dlHint = document.getElementById('product-allow-download-hint');
        if (dlLabel) dlLabel.textContent = i18n.t('admin_products_allow_download');
        if (dlHint) dlHint.textContent = i18n.t('admin_products_allow_download_hint');
        // Translate all data-i18n elements within the products tab
        var tab = document.getElementById('admin-tab-products');
        if (tab) {
            tab.querySelectorAll('[data-i18n]').forEach(function (el) {
                el.textContent = i18n.t(el.getAttribute('data-i18n'));
            });
            tab.querySelectorAll('[data-i18n-placeholder]').forEach(function (el) {
                el.placeholder = i18n.t(el.getAttribute('data-i18n-placeholder'));
            });
        }
        adminFetch('/api/products')
            .then(function (res) {
                if (!res.ok) throw new Error('load failed');
                return res.json();
            })
            .then(function (data) {
                renderProducts(data.products || []);
            })
            .catch(function () {
                renderProducts([]);
            });
    }

    function renderProducts(products) {
        var tbody = document.getElementById('admin-products-tbody');
        if (!tbody) return;

        // Cache products for edit access
        window._cachedProducts = products;

        if (!products || products.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6" class="admin-table-empty">' + i18n.t('admin_products_empty') + '</td></tr>';
            return;
        }

        var html = '';
        for (var i = 0; i < products.length; i++) {
            var p = products[i];
            var createdAt = p.created_at ? new Date(p.created_at).toLocaleString() : '-';
            var typeLabel = p.type === 'knowledge_base' ? i18n.t('admin_products_type_knowledge') : i18n.t('admin_products_type_service');
            var dlLabel = p.allow_download ? '✅' : '❌';
            html += '<tr>' +
                '<td>' + escapeHtml(p.name) + '</td>' +
                '<td>' + escapeHtml(typeLabel) + '</td>' +
                '<td>' + escapeHtml(p.description || '-') + '</td>' +
                '<td>' + dlLabel + '</td>' +
                '<td>' + escapeHtml(createdAt) + '</td>' +
                '<td>' +
                    '<button class="btn-primary btn-sm" style="margin-right:6px" onclick="editProduct(\'' + escapeHtml(p.id) + '\')">' + i18n.t('admin_products_edit_btn') + '</button>' +
                    '<button class="btn-danger btn-sm" onclick="deleteProduct(\'' + escapeHtml(p.id) + '\', \'' + escapeHtml(p.name) + '\')">' + i18n.t('admin_products_delete_btn') + '</button>' +
                '</td>' +
            '</tr>';
        }
        tbody.innerHTML = html;
    }

    window.createProduct = function () {
        var name = (document.getElementById('product-new-name') || {}).value || '';
        var productType = (document.getElementById('product-new-type') || {}).value || 'service';
        var desc = (document.getElementById('product-new-desc') || {}).value || '';
        var welcome = (document.getElementById('product-new-welcome') || {}).value || '';
        var allowDownload = document.getElementById('product-new-allow-download') ? document.getElementById('product-new-allow-download').checked : false;

        if (!name.trim()) {
            showAdminToast(i18n.t('admin_products_name_required'), 'error');
            return;
        }

        adminFetch('/api/products', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: name.trim(), type: productType, description: desc.trim(), welcome_message: welcome.trim(), allow_download: allowDownload })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_products_create_failed')); });
            return res.json();
        })
        .then(function () {
            showAdminToast(i18n.t('admin_products_created'), 'success');
            if (document.getElementById('product-new-name')) document.getElementById('product-new-name').value = '';
            if (document.getElementById('product-new-type')) document.getElementById('product-new-type').value = 'service';
            if (document.getElementById('product-new-desc')) document.getElementById('product-new-desc').value = '';
            if (document.getElementById('product-new-welcome')) document.getElementById('product-new-welcome').value = '';
            if (document.getElementById('product-new-allow-download')) document.getElementById('product-new-allow-download').checked = false;
            loadProducts();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_products_create_failed'), 'error');
        });
    };

    window.deleteProduct = function (id, name) {
        if (!confirm(i18n.t('admin_products_delete_confirm', { name: name }))) return;

        adminFetch('/api/products/' + encodeURIComponent(id) + '?confirm=true', {
            method: 'DELETE'
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_delete_failed')); });
            showAdminToast(i18n.t('admin_products_deleted'), 'success');
            loadProducts();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_delete_failed'), 'error');
        });
    };

    window.editProduct = function (id) {
        var products = window._cachedProducts || [];
        var p = null;
        for (var i = 0; i < products.length; i++) {
            if (products[i].id === id) { p = products[i]; break; }
        }
        if (!p) return;

        var modal = document.getElementById('product-edit-modal');
        if (!modal) return;

        document.getElementById('product-edit-id').value = p.id;
        document.getElementById('product-edit-name').value = p.name;
        var typeDisplay = p.type === 'knowledge_base' ? i18n.t('admin_products_type_knowledge') : i18n.t('admin_products_type_service');
        document.getElementById('product-edit-type').value = typeDisplay;
        document.getElementById('product-edit-type').setAttribute('data-raw-type', p.type);
        document.getElementById('product-edit-desc').value = p.description || '';
        document.getElementById('product-edit-welcome').value = p.welcome_message || '';
        document.getElementById('product-edit-allow-download').checked = !!p.allow_download;

        // Update modal title
        var titleEl = document.getElementById('product-edit-modal-title');
        if (titleEl) titleEl.textContent = i18n.t('admin_products_edit_title');
        var saveBtn = document.getElementById('product-edit-save-btn');
        if (saveBtn) saveBtn.textContent = i18n.t('admin_products_edit_save');
        var cancelBtn = document.getElementById('product-edit-cancel-btn');
        if (cancelBtn) cancelBtn.textContent = i18n.t('admin_products_edit_cancel');

        // Apply i18n labels inside modal
        modal.querySelectorAll('[data-i18n]').forEach(function (el) {
            el.textContent = i18n.t(el.getAttribute('data-i18n'));
        });
        modal.querySelectorAll('[data-i18n-placeholder]').forEach(function (el) {
            el.placeholder = i18n.t(el.getAttribute('data-i18n-placeholder'));
        });

        modal.style.display = 'flex';

        // Close on overlay click
        modal.onclick = function (e) {
            if (e.target === modal) window.closeProductEditModal();
        };
    };

    window.closeProductEditModal = function () {
        var modal = document.getElementById('product-edit-modal');
        if (modal) modal.style.display = 'none';
    };

    window.saveProductEdit = function () {
        var id = document.getElementById('product-edit-id').value;
        var name = document.getElementById('product-edit-name').value.trim();
        var productType = document.getElementById('product-edit-type').getAttribute('data-raw-type') || document.getElementById('product-edit-type').value;
        var desc = document.getElementById('product-edit-desc').value.trim();
        var welcome = document.getElementById('product-edit-welcome').value.trim();
        var allowDownload = document.getElementById('product-edit-allow-download').checked;

        if (!name) {
            showAdminToast(i18n.t('admin_products_name_required'), 'error');
            return;
        }

        adminFetch('/api/products/' + encodeURIComponent(id), {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: name, type: productType, description: desc, welcome_message: welcome, allow_download: allowDownload })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_products_edit_failed')); });
            return res.json();
        })
        .then(function () {
            showAdminToast(i18n.t('admin_products_edit_success'), 'success');
            window.closeProductEditModal();
            loadProducts();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_products_edit_failed'), 'error');
        });
    };

    // Load product checkboxes for admin user creation form
    function loadProductCheckboxes() {
        var container = document.getElementById('admin-new-products-checkboxes');
        if (!container) return;

        adminFetch('/api/products')
            .then(function (res) {
                if (!res.ok) throw new Error('load failed');
                return res.json();
            })
            .then(function (data) {
                var products = data.products || [];
                if (products.length === 0) {
                    container.innerHTML = '<span class="admin-form-hint">' + i18n.t('admin_products_empty') + '</span>';
                    return;
                }
                var html = '';
                for (var i = 0; i < products.length; i++) {
                    var p = products[i];
                    html += '<label class="admin-checkbox-label">' +
                        '<input type="checkbox" name="admin-new-product" value="' + escapeHtml(p.id) + '"> ' +
                        escapeHtml(p.name) +
                    '</label>';
                }
                container.innerHTML = html;
            })
            .catch(function () {
                container.innerHTML = '<span class="admin-form-hint">' + i18n.t('admin_products_load_failed') + '</span>';
            });
    }

    function getSelectedProductIDs() {
        var checkboxes = document.querySelectorAll('input[name="admin-new-product"]:checked');
        var ids = [];
        checkboxes.forEach(function (cb) { ids.push(cb.value); });
        return ids;
    }

    // --- Admin User Management ---

    function loadAdminUsers() {
        adminFetch('/api/admin/users')
            .then(function (res) {
                if (!res.ok) throw new Error(i18n.t('admin_doc_load_failed'));
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
            tbody.innerHTML = '<tr><td colspan="6" class="admin-table-empty">' + i18n.t('admin_users_empty') + '</td></tr>';
            return;
        }

        var roleMap = { 'editor': i18n.t('admin_users_role_editor_short'), 'super_admin': i18n.t('admin_users_role_super_short') };
        var permMap = { 'batch_import': i18n.t('admin_users_perm_batch_import') || '批量导入' };
        var html = '';
        for (var i = 0; i < users.length; i++) {
            var u = users[i];
            var productNames = (u.product_names && u.product_names.length > 0) ? u.product_names.map(escapeHtml).join(', ') : i18n.t('admin_users_all_products');
            var permNames = '';
            if (u.role === 'super_admin') {
                permNames = i18n.t('admin_users_all_permissions') || '全部';
            } else if (u.permissions && u.permissions.length > 0) {
                permNames = u.permissions.map(function(p) { return escapeHtml(permMap[p] || p); }).join(', ');
            } else {
                permNames = '-';
            }
            html += '<tr>' +
                '<td>' + escapeHtml(u.username) + '</td>' +
                '<td>' + escapeHtml(roleMap[u.role] || u.role) + '</td>' +
                '<td>' + productNames + '</td>' +
                '<td>' + permNames + '</td>' +
                '<td>' + escapeHtml(u.created_at || '-') + '</td>' +
                '<td><button class="btn-danger btn-sm" onclick="deleteAdminUser(\'' + escapeHtml(u.id) + '\', \'' + escapeHtml(u.username) + '\')">' + i18n.t('admin_users_delete_btn') + '</button></td>' +
            '</tr>';
        }
        tbody.innerHTML = html;
    }

    window.createAdminUser = function () {
        var username = (document.getElementById('admin-new-username') || {}).value || '';
        var password = (document.getElementById('admin-new-password') || {}).value || '';
        var role = (document.getElementById('admin-new-role') || {}).value || 'editor';
        var productIDs = getSelectedProductIDs();

        var permissions = [];
        var batchImportCb = document.getElementById('admin-new-perm-batch-import');
        if (batchImportCb && batchImportCb.checked) {
            permissions.push('batch_import');
        }

        if (!username.trim() || !password) {
            showAdminToast(i18n.t('admin_users_create_empty'), 'error');
            return;
        }
        if (password.length < 6) {
            showAdminToast(i18n.t('admin_users_create_password_short'), 'error');
            return;
        }

        adminFetch('/api/admin/users', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username.trim(), password: password, role: role, product_ids: productIDs, permissions: permissions })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_users_create_failed')); });
            return res.json();
        })
        .then(function () {
            showAdminToast(i18n.t('admin_users_created'), 'success');
            if (document.getElementById('admin-new-username')) document.getElementById('admin-new-username').value = '';
            if (document.getElementById('admin-new-password')) document.getElementById('admin-new-password').value = '';
            var batchCb = document.getElementById('admin-new-perm-batch-import');
            if (batchCb) batchCb.checked = false;
            loadAdminUsers();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_users_create_failed'), 'error');
        });
    };

    window.deleteAdminUser = function (id, username) {
        if (!confirm(i18n.t('admin_users_delete_confirm', { name: username }))) return;

        adminFetch('/api/admin/users/' + encodeURIComponent(id), {
            method: 'DELETE'
        })
        .then(function (res) {
            if (!res.ok) throw new Error(i18n.t('admin_delete_failed'));
            showAdminToast(i18n.t('admin_users_deleted'), 'success');
            loadAdminUsers();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_delete_failed'), 'error');
        });
    };

    // --- Login Ban Management ---

    function loadLoginBans() {
        adminFetch('/api/admin/bans')
            .then(function (res) {
                if (!res.ok) throw new Error('load failed');
                return res.json();
            })
            .then(function (data) {
                renderLoginBans(data.bans || []);
            })
            .catch(function () {
                renderLoginBans([]);
            });
    }

    function renderLoginBans(bans) {
        var tbody = document.getElementById('admin-bans-tbody');
        if (!tbody) return;

        if (!bans || bans.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6" class="admin-table-empty">' + i18n.t('admin_bans_empty') + '</td></tr>';
            return;
        }

        var typeMap = {
            'user_consecutive': i18n.t('admin_bans_type_consecutive'),
            'user_daily': i18n.t('admin_bans_type_daily'),
            'ip': i18n.t('admin_bans_type_ip'),
            'manual_user': i18n.t('admin_bans_type_manual_user'),
            'manual_ip': i18n.t('admin_bans_type_manual_ip')
        };

        var html = '';
        for (var i = 0; i < bans.length; i++) {
            var b = bans[i];
            var target = b.username ? (i18n.t('admin_bans_user_prefix') + escapeHtml(b.username)) : (i18n.t('admin_bans_ip_prefix') + escapeHtml(b.ip));
            var unlocks = b.unlocks_at ? new Date(b.unlocks_at).toLocaleString() : '-';
            var unbanData = b.username ? "'" + escapeHtml(b.username) + "',''" : "''," + "'" + escapeHtml(b.ip) + "'";
            html += '<tr>' +
                '<td>' + escapeHtml(typeMap[b.type] || b.type) + '</td>' +
                '<td>' + target + '</td>' +
                '<td>' + escapeHtml(b.reason || '-') + '</td>' +
                '<td>' + (b.fail_count || '-') + '</td>' +
                '<td>' + escapeHtml(unlocks) + '</td>' +
                '<td><button class="btn-danger btn-sm" onclick="unbanLogin(' + unbanData + ')">' + i18n.t('admin_bans_unban_btn') + '</button></td>' +
            '</tr>';
        }
        tbody.innerHTML = html;
    }

    window.unbanLogin = function (username, ip) {
        if (!confirm(i18n.t('admin_bans_unban_confirm'))) return;

        adminFetch('/api/admin/bans/unban', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username, ip: ip })
        })
        .then(function (res) {
            if (!res.ok) throw new Error('failed');
            showAdminToast(i18n.t('admin_bans_unbanned'), 'success');
            loadLoginBans();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_bans_unban_failed'), 'error');
        });
    };

    window.addLoginBan = function () {
        var username = (document.getElementById('ban-new-username') || {}).value || '';
        var ip = (document.getElementById('ban-new-ip') || {}).value || '';
        var reason = (document.getElementById('ban-new-reason') || {}).value || '';
        var days = parseInt((document.getElementById('ban-new-days') || {}).value) || 1;

        if (!username.trim() && !ip.trim()) {
            showAdminToast(i18n.t('admin_bans_add_empty'), 'error');
            return;
        }

        adminFetch('/api/admin/bans/add', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username: username.trim(), ip: ip.trim(), reason: reason.trim(), days: days })
        })
        .then(function (res) {
            if (!res.ok) return res.json().then(function (d) { throw new Error(d.error || i18n.t('admin_bans_add_failed')); });
            return res.json();
        })
        .then(function () {
            showAdminToast(i18n.t('admin_bans_added'), 'success');
            if (document.getElementById('ban-new-username')) document.getElementById('ban-new-username').value = '';
            if (document.getElementById('ban-new-ip')) document.getElementById('ban-new-ip').value = '';
            if (document.getElementById('ban-new-reason')) document.getElementById('ban-new-reason').value = '';
            if (document.getElementById('ban-new-days')) document.getElementById('ban-new-days').value = '1';
            loadLoginBans();
        })
        .catch(function (err) {
            showAdminToast(err.message || i18n.t('admin_bans_add_failed'), 'error');
        });
    };

    // --- Logout ---

    window.logout = function () {
        chatMessages = [];
        chatLoading = false;
        localStorage.removeItem('askflow_product_id');
        localStorage.removeItem('askflow_product_name');
        clearSession();
        navigate('/login');
    };

    window.adminLogout = function () {
        adminRole = '';
        adminPermissions = [];
        localStorage.removeItem('admin_role');
        clearAdminSession();
        navigate(adminLoginRoute);
    };

    // --- Init ---

    function init() {
        // Check for OAuth callback first
        if (handleOAuthCallbackFromURL()) return;

        // Check for ticket-login callback (SN login via desktop app)
        if (handleTicketLoginFromURL()) return;

        // Pre-warm product cache early (used by login page and chat page)
        fetchProducts();

        // Fetch app-info in parallel (non-blocking, doesn't affect routing)
        fetch('/api/app-info')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (data.oauth_providers) {
                    renderOAuthLoginButtons(data.oauth_providers);
                }
                if (data.max_upload_size_mb) {
                    maxUploadSizeMB = data.max_upload_size_mb;
                }
            })
            .catch(function () { /* ignore */ });

        // system/status and admin/status affect routing, so wait for them before rendering
        var p1 = fetch('/api/system/status')
            .then(function (res) { return res.json(); })
            .then(function (data) { systemReady = !!data.ready; })
            .catch(function () { systemReady = true; });

        var p2 = fetch('/api/admin/status')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (data.login_route) adminLoginRoute = data.login_route;
            })
            .catch(function () { /* use default */ });

        Promise.all([p1, p2]).then(function () {
            handleRoute();
            // Fetch translated product name in background (LLM call, can be slow)
            fetchProductName();
        }).catch(function () {
            handleRoute();
        });
    }

    // Fetch product name from server and apply to UI
    function fetchProductName() {
        var lang = window.i18n ? window.i18n.getLang() : 'zh-CN';
        return fetch('/api/translate-product-name?lang=' + encodeURIComponent(lang))
            .then(function (res) { return res.json(); })
            .then(function (data) {
                if (data.product_name) {
                    applyProductName(data.product_name);
                }
            })
            .catch(function () { /* use default i18n */ });
    }

    // Apply product name to all relevant UI elements
    function applyProductName(name) {
        if (!name) return;
        window._productName = name;

        // Update page title
        document.title = name;

        // Update elements with data-product-name attribute
        var els = document.querySelectorAll('[data-product-name]');
        els.forEach(function (el) {
            el.textContent = name;
        });

        // Update welcome title: "欢迎使用 + name"
        var welcomeEls = document.querySelectorAll('[data-product-name-welcome]');
        var lang = window.i18n ? window.i18n.getLang() : 'zh-CN';
        var welcomePrefix = lang === 'en-US' ? 'Welcome to ' : '欢迎使用';
        welcomeEls.forEach(function (el) {
            el.textContent = welcomePrefix + name;
        });
    }

    // Override i18n.applyI18nToPage to also refresh product name
    var _origApplyI18n = window.i18n ? window.i18n.applyI18nToPage : null;
    if (window.i18n) {
        window.i18n.applyI18nToPage = function () {
            if (_origApplyI18n) _origApplyI18n();
            // Re-fetch translated product name when language changes
            fetchProductName();
        };
    }

    // --- Batch Import ---

    function loadBatchImportProductSelector() {
        adminFetch('/api/products/my')
            .then(function (res) { return res.json(); })
            .then(function (data) {
                var sel = document.getElementById('batch-product-select');
                if (!sel) return;
                var products = data.products || [];
                sel.innerHTML = '<option value="">公共区</option>';
                products.forEach(function (p) {
                    sel.innerHTML += '<option value="' + p.id + '">' + p.name + '</option>';
                });
            })
            .catch(function () {});
    }
    window.loadBatchImportProductSelector = loadBatchImportProductSelector;

    window.startBatchImport = function () {
        var pathInput = document.getElementById('batch-import-path');
        var importPath = (pathInput.value || '').trim();
        if (!importPath) {
            showAdminToast('请输入文件或目录路径', 'error');
            return;
        }

        var productID = document.getElementById('batch-product-select').value || '';
        var btn = document.getElementById('batch-import-btn');
        btn.disabled = true;
        btn.textContent = '导入中...';
        // Reset UI
        var progressSection = document.getElementById('batch-progress-section');
        var reportSection = document.getElementById('batch-report-section');
        progressSection.classList.remove('hidden');
        reportSection.classList.add('hidden');
        document.getElementById('batch-progress-log').innerHTML = '';
        document.getElementById('batch-progress-fill').style.width = '0%';
        document.getElementById('batch-progress-text').textContent = '准备中...';        document.getElementById('batch-progress-percent').textContent = '0%';

        var token = getAdminToken();

        fetch('/api/batch-import', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Authorization': 'Bearer ' + token
            },
            body: JSON.stringify({ path: importPath, product_id: productID })
        }).then(function (response) {
            if (!response.ok) {
                return response.json().then(function (d) {
                    throw new Error(d.error || 'HTTP ' + response.status);
                });
            }

            var reader = response.body.getReader();
            var decoder = new TextDecoder();
            var buffer = '';

            function processChunk(result) {
                if (result.done) return;
                buffer += decoder.decode(result.value, { stream: true });

                var lines = buffer.split('\n');
                buffer = lines.pop(); // keep incomplete line in buffer

                var currentEvent = '';
                for (var i = 0; i < lines.length; i++) {
                    var line = lines[i];
                    if (line.indexOf('event: ') === 0) {
                        currentEvent = line.substring(7);
                    } else if (line.indexOf('data: ') === 0) {
                        var jsonStr = line.substring(6);
                        try {
                            var data = JSON.parse(jsonStr);
                            handleSSEEvent(currentEvent, data);
                        } catch (e) {}
                    }
                }

                return reader.read().then(processChunk);
            }

            return reader.read().then(processChunk);
        }).catch(function (err) {
            showAdminToast('批量导入失败: ' + err.message, 'error');
        }).finally(function () {
            btn.disabled = false;
            btn.textContent = '开始导入';
        });
    };

    function handleSSEEvent(event, data) {
        var logEl = document.getElementById('batch-progress-log');
        var fillEl = document.getElementById('batch-progress-fill');
        var textEl = document.getElementById('batch-progress-text');
        var percentEl = document.getElementById('batch-progress-percent');

        if (event === 'start') {
            textEl.textContent = '共 ' + data.total + ' 个文件，开始导入...';        } else if (event === 'progress') {
            var pct = Math.round((data.index / data.total) * 100);
            fillEl.style.width = pct + '%';
            percentEl.textContent = pct + '%';
            textEl.textContent = '[' + data.index + '/' + data.total + '] ' + data.file;

            var item = document.createElement('div');
            item.className = 'log-item';
            if (data.status === 'success') {
                item.className += ' log-success';
                item.textContent = '[' + data.index + '/' + data.total + '] ✅ ' + data.file;
            } else {
                item.className += ' log-failed';
                item.textContent = '[' + data.index + '/' + data.total + '] ❌ ' + data.file + ' — ' + data.reason;
            }
            logEl.appendChild(item);
            logEl.scrollTop = logEl.scrollHeight;
        } else if (event === 'done') {
            textEl.textContent = '导入完成';
            percentEl.textContent = '100%';
            fillEl.style.width = '100%';
            showBatchReport(data);
        }
    }

    function showBatchReport(data) {
        var reportSection = document.getElementById('batch-report-section');
        reportSection.classList.remove('hidden');

        var summary = document.getElementById('batch-report-summary');
        summary.innerHTML =
            '<div class="batch-report-stat stat-total"><span class="stat-value">' + data.total + '</span><span class="stat-label">总文件数</span></div>' +
            '<div class="batch-report-stat stat-success"><span class="stat-value">' + data.success + '</span><span class="stat-label">成功</span></div>' +
            '<div class="batch-report-stat stat-failed"><span class="stat-value">' + data.failed + '</span><span class="stat-label">失败</span></div>';

        var failedSection = document.getElementById('batch-report-failed');
        var failedList = document.getElementById('batch-report-failed-list');

        if (data.failed_files && data.failed_files.length > 0) {
            failedSection.classList.remove('hidden');
            failedList.innerHTML = '';
            data.failed_files.forEach(function (f) {
                var item = document.createElement('div');
                item.className = 'batch-failed-item';
                item.innerHTML = '<div class="failed-path">' + escapeHtml(f.path) + '</div>' +
                    '<div class="failed-reason">' + escapeHtml(f.reason) + '</div>';
                failedList.appendChild(item);
            });
        } else {
            failedSection.classList.add('hidden');
        }
    }

    function escapeHtml(str) {
        var div = document.createElement('div');
        div.appendChild(document.createTextNode(str));
        return div.innerHTML;
    }

    // Run on DOM ready
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

})();
